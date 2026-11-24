package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// findFFmpeg looks next to the program first, then on PATH.
func findFFmpeg(exeDir string) string {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	for _, p := range []string{
		filepath.Join(exeDir, name),
		filepath.Join(exeDir, "ffmpeg", name),
		filepath.Join(exeDir, "ffmpeg", "bin", name),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

var (
	reDuration = regexp.MustCompile(`Duration: (\d+):(\d+):(\d+(?:\.\d+)?)`)
	reVideo    = regexp.MustCompile(`Stream #\d+:\d+.*?: Video: .*?, (\d{2,5})x(\d{2,5})`)
	reFPS      = regexp.MustCompile(`, (\d+(?:\.\d+)?) fps`)
	reAudio    = regexp.MustCompile(`Stream #\d+:\d+.*?: Audio: `)
	reRotate   = regexp.MustCompile(`rotation of (-?\d+(?:\.\d+)?) degrees`)
)

// probe reads duration, size, fps and audio presence from `ffmpeg -i` output,
// so no ffprobe binary is needed.
func probe(ffmpeg, path string) (*Clip, error) {
	cmd := exec.Command(ffmpeg, "-hide_banner", "-i", path)
	hideWindow(cmd)
	out, _ := cmd.CombinedOutput() // exits 1 because no output file is given
	s := string(out)

	vm := reVideo.FindStringSubmatch(s)
	dm := reDuration.FindStringSubmatch(s)
	if vm == nil || dm == nil {
		return nil, errors.New("no video stream")
	}
	h, _ := strconv.Atoi(dm[1])
	m, _ := strconv.Atoi(dm[2])
	sec, _ := strconv.ParseFloat(dm[3], 64)
	c := &Clip{Duration: float64(h*3600+m*60) + sec}
	c.Width, _ = strconv.Atoi(vm[1])
	c.Height, _ = strconv.Atoi(vm[2])

	// Phone videos store rotation as metadata; FFmpeg auto-rotates them.
	if rm := reRotate.FindStringSubmatch(s); rm != nil {
		deg, _ := strconv.ParseFloat(rm[1], 64)
		if int(math.Abs(deg))%180 == 90 {
			c.Width, c.Height = c.Height, c.Width
		}
	}

	videoLine := s[strings.Index(s, vm[0]):]
	if i := strings.IndexByte(videoLine, '\n'); i > 0 {
		videoLine = videoLine[:i]
	}
	if fm := reFPS.FindStringSubmatch(videoLine); fm != nil {
		c.FPS = fm[1]
	}
	c.HasAudio = reAudio.MatchString(s)
	if c.Duration <= 0 {
		return nil, errors.New("zero length")
	}
	return c, nil
}

type exportJob struct {
	cancel context.CancelFunc
	out    string
}

func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs      []string `json:"ids"`
		Settings Settings `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		http.Error(w, "add some clips first", 400)
		return
	}

	a.mu.Lock()
	if a.export != nil {
		a.mu.Unlock()
		http.Error(w, "an export is already running", 409)
		return
	}
	if a.ffmpeg == "" {
		a.mu.Unlock()
		http.Error(w, "FFmpeg not found. Put ffmpeg next to the program.", 500)
		return
	}
	var clips []*Clip
	for _, id := range req.IDs {
		c, ok := a.clips[id]
		if !ok {
			a.mu.Unlock()
			http.Error(w, "a clip is missing, add it again", 400)
			return
		}
		clips = append(clips, c)
	}
	a.settings = req.Settings
	a.saveSettings()
	s := a.settings
	ctx, cancel := context.WithCancel(context.Background())
	outDir := s.OutputDir
	if outDir == "" {
		outDir = filepath.Join(a.exeDir, "output")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		a.mu.Unlock()
		cancel()
		http.Error(w, "cannot use output folder: "+err.Error(), 400)
		return
	}
	out := filepath.Join(outDir, "merged_"+time.Now().Format("2006-01-02_15-04-05")+".mp4")
	a.export = &exportJob{cancel: cancel, out: out}
	a.mu.Unlock()

	go a.runExport(ctx, clips, s, out)
	writeJSON(w, map[string]string{"out": out})
}

func (a *App) handleCancel(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if a.export != nil {
		a.export.cancel()
	}
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) runExport(ctx context.Context, clips []*Clip, s Settings, out string) {
	defer func() {
		a.mu.Lock()
		a.export = nil
		a.mu.Unlock()
	}()

	args, total := buildArgs(clips, s, out)
	if s.Text != "" {
		os.WriteFile(filepath.Join(a.tmpDir, "wm.txt"), []byte(s.Text), 0o644)
	}

	cmd := exec.CommandContext(ctx, a.ffmpeg, args...)
	cmd.Dir = a.tmpDir // font.ttf and wm.txt are referenced by relative name
	hideWindow(cmd)
	stdout, _ := cmd.StdoutPipe()
	var stderr tailBuffer
	cmd.Stderr = &stderr

	a.broadcast(map[string]any{"type": "progress", "pct": 0})
	if err := cmd.Start(); err != nil {
		a.broadcast(map[string]any{"type": "error", "msg": err.Error()})
		return
	}
	sc := bufio.NewScanner(stdout)
	lastPct := -1
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "out_time_us="); ok {
			us, err := strconv.ParseFloat(v, 64)
			if err != nil || total <= 0 {
				continue
			}
			pct := int(us / 1e6 / total * 100)
			pct = max(0, min(99, pct))
			if pct != lastPct {
				lastPct = pct
				a.broadcast(map[string]any{"type": "progress", "pct": pct})
			}
		}
	}
	err := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		os.Remove(out)
		a.broadcast(map[string]any{"type": "cancelled"})
	case err != nil:
		os.Remove(out)
		a.broadcast(map[string]any{"type": "error", "msg": stderr.String()})
	default:
		a.broadcast(map[string]any{"type": "done", "path": out})
	}
}

// buildArgs makes one FFmpeg pass: scale/pad every clip to the first clip's
// size, give silent audio to clips without sound, join, then draw the text.
func buildArgs(clips []*Clip, s Settings, out string) ([]string, float64) {
	W, H := even(clips[0].Width), even(clips[0].Height)
	fps := clips[0].FPS
	if f, err := strconv.ParseFloat(fps, 64); err != nil || f <= 0 || f > 120 {
		fps = "30"
	}

	args := []string{"-hide_banner", "-y", "-nostats", "-progress", "pipe:1"}
	for _, c := range clips {
		args = append(args, "-i", c.Path)
	}

	var fg strings.Builder
	var total float64
	for i, c := range clips {
		total += c.Duration
		d := strconv.FormatFloat(c.Duration, 'f', 3, 64)
		fmt.Fprintf(&fg, "[%d:v:0]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=%s,format=yuv420p,trim=duration=%s,setpts=PTS-STARTPTS[v%d];",
			i, W, H, W, H, fps, d, i)
		if c.HasAudio {
			fmt.Fprintf(&fg, "[%d:a:0]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,apad,atrim=duration=%s,asetpts=PTS-STARTPTS[a%d];", i, d, i)
		} else {
			fmt.Fprintf(&fg, "anullsrc=r=48000:cl=stereo,atrim=duration=%s,aformat=sample_fmts=fltp[a%d];", d, i)
		}
	}
	for i := range clips {
		fmt.Fprintf(&fg, "[v%d][a%d]", i, i)
	}
	fmt.Fprintf(&fg, "concat=n=%d:v=1:a=1[cv][ca]", len(clips))

	if strings.TrimSpace(s.Text) != "" {
		fontSize := int(math.Round(s.Size / 100 * float64(H)))
		margin := int(math.Round(s.Margin / 100 * float64(H)))
		fx, fy := clamp01(s.FX), clamp01(s.FY)
		fmt.Fprintf(&fg, ";[cv]drawtext=fontfile=font.ttf:textfile=wm.txt:expansion=none:y_align=font:fontsize=%d:fontcolor=%s@%.2f:x=%d+%.4f*(w-tw-%d):y=%d+%.4f*(h-th-%d)[outv]",
			max(fontSize, 1), ffColor(s.Color), clamp01(s.Opacity/100), margin, fx, 2*margin, margin, fy, 2*margin)
	} else {
		fg.WriteString(";[cv]null[outv]")
	}

	args = append(args,
		"-filter_complex", fg.String(),
		"-map", "[outv]", "-map", "[ca]",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-profile:v", "high",
		"-c:a", "aac", "-b:a", "192k",
		"-movflags", "+faststart",
		out,
	)
	return args, total
}

func even(n int) int {
	if n < 2 {
		return 2
	}
	return n &^ 1
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

var reHex = regexp.MustCompile(`^#?([0-9a-fA-F]{6})$`)

func ffColor(c string) string {
	if m := reHex.FindStringSubmatch(c); m != nil {
		return "0x" + m[1]
	}
	return "white"
}

// tailBuffer keeps the last few KB of FFmpeg's error output.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4000 {
		t.b = t.b[len(t.b)-4000:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n")
}
