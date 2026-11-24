// Video Merger: a tiny local web app that joins short clips into one video
// and burns a text watermark on it with FFmpeg.
//
// Double-click to start. It opens the browser. When the last browser tab
// closes, the program exits and deletes its temp files.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFiles embed.FS

// Settings are saved to settings.json so the watermark is set up only once.
type Settings struct {
	Text      string  `json:"text"`
	FX        float64 `json:"fx"`      // 0 = left, 1 = right
	FY        float64 `json:"fy"`      // 0 = top, 1 = bottom
	Size      float64 `json:"size"`    // font size, % of video height
	Margin    float64 `json:"margin"`  // edge gap, % of video height
	Color     string  `json:"color"`   // #rrggbb
	Opacity   float64 `json:"opacity"` // 0-100
	OutputDir string  `json:"outputDir"`
	PanelOpen bool    `json:"panelOpen"`
	TimelineH float64 `json:"timelineH"` // timeline height in px, 0 = default
}

type Clip struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Path     string  `json:"-"`
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	FPS      string  `json:"fps"`
	HasAudio bool    `json:"hasAudio"`
}

type App struct {
	mu           sync.Mutex
	exeDir       string
	tmpDir       string
	settingsPath string
	settings     Settings
	clips        map[string]*Clip
	ffmpeg       string

	clients  map[chan string]bool
	lastSeen time.Time
	everSeen bool

	export *exportJob
}

func main() {
	app := &App{clients: map[chan string]bool{}, clips: map[string]*Clip{}}
	app.init()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("cannot start local server: %v", err)
	}
	addr := ln.Addr().String()
	url := "http://" + addr + "/"

	mux := http.NewServeMux()
	webRoot, _ := fs.Sub(webFiles, "web")
	mux.Handle("/", http.FileServer(http.FS(webRoot)))
	mux.HandleFunc("/api/state", app.handleState)
	mux.HandleFunc("/api/events", app.handleEvents)
	mux.HandleFunc("/api/settings", app.handleSettings)
	mux.HandleFunc("/api/upload", app.handleUpload)
	mux.HandleFunc("/api/remove", app.handleRemove)
	mux.HandleFunc("/api/export", app.handleExport)
	mux.HandleFunc("/api/cancel", app.handleCancel)
	mux.HandleFunc("/api/pick-folder", app.handlePickFolder)
	mux.HandleFunc("/api/open-folder", app.handleOpenFolder)

	srv := &http.Server{Handler: guard(addr, mux)}
	go srv.Serve(ln)

	log.Printf("Video Merger running at %s", url)
	if err := openBrowser(url); err != nil {
		log.Printf("open the page yourself: %s", url)
	}

	go app.watchdog()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() { <-sig; app.quit() }()
	select {}
}

func (a *App) init() {
	exe, err := os.Executable()
	if err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		a.exeDir = filepath.Dir(exe)
	} else {
		a.exeDir, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(a.exeDir); err == nil {
		a.exeDir = abs
	}

	a.tmpDir, err = os.MkdirTemp("", "videomerger-")
	if err != nil {
		fatal("cannot create temp folder: %v", err)
	}
	os.MkdirAll(filepath.Join(a.tmpDir, "clips"), 0o755)
	font, _ := webFiles.ReadFile("web/font.ttf")
	os.WriteFile(filepath.Join(a.tmpDir, "font.ttf"), font, 0o644)

	a.ffmpeg = findFFmpeg(a.exeDir)
	a.loadSettings()
}

// Settings live next to the program so the whole folder stays portable.
// If that folder is read-only, fall back to the user config folder.
func (a *App) loadSettings() {
	a.settings = Settings{
		Text: "My Page", FX: 1, FY: 1, Size: 5, Margin: 3,
		Color: "#ffffff", Opacity: 70,
		OutputDir: filepath.Join(a.exeDir, "output"), PanelOpen: true,
	}
	a.settingsPath = filepath.Join(a.exeDir, "settings.json")
	if !writable(a.exeDir) {
		if cfg, err := os.UserConfigDir(); err == nil {
			dir := filepath.Join(cfg, "VideoMerger")
			os.MkdirAll(dir, 0o755)
			a.settingsPath = filepath.Join(dir, "settings.json")
		}
	}
	if b, err := os.ReadFile(a.settingsPath); err == nil {
		json.Unmarshal(b, &a.settings)
	}
}

func (a *App) saveSettings() error {
	b, _ := json.MarshalIndent(a.settings, "", "  ")
	return os.WriteFile(a.settingsPath, b, 0o644)
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".wtest-")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// watchdog exits the program once no browser tab is connected.
func (a *App) watchdog() {
	start := time.Now()
	for range time.Tick(time.Second) {
		a.mu.Lock()
		n := len(a.clients)
		idle := time.Since(a.lastSeen)
		ever := a.everSeen
		a.mu.Unlock()
		if n > 0 {
			continue
		}
		if ever && idle > 6*time.Second {
			a.quit()
		}
		if !ever && time.Since(start) > 2*time.Minute {
			a.quit()
		}
	}
}

func (a *App) quit() {
	a.mu.Lock()
	if a.export != nil {
		a.export.cancel()
	}
	a.mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	os.RemoveAll(a.tmpDir)
	os.Exit(0)
}

func (a *App) broadcast(v any) {
	b, _ := json.Marshal(v)
	a.mu.Lock()
	defer a.mu.Unlock()
	for c := range a.clients {
		select {
		case c <- string(b):
		default:
		}
	}
}

// guard blocks requests from other websites (DNS rebinding / CSRF).
func guard(addr string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != addr {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost && r.Header.Get("X-App") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- handlers ----------

func (a *App) handleState(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeJSON(w, map[string]any{
		"settings":  a.settings,
		"hasFFmpeg": a.ffmpeg != "",
		"exporting": a.export != nil,
	})
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	c := make(chan string, 64)
	a.mu.Lock()
	a.clients[c] = true
	a.everSeen = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.clients, c)
		a.lastSeen = time.Now()
		a.mu.Unlock()
	}()

	fmt.Fprint(w, ": hi\n\n")
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-c:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var s Settings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, "bad settings", 400)
		return
	}
	a.mu.Lock()
	a.settings = s
	err := a.saveSettings()
	a.mu.Unlock()
	if err != nil {
		http.Error(w, "could not save settings: "+err.Error(), 500)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	name := filepath.Base(r.URL.Query().Get("name"))
	id := randID()
	ext := strings.ToLower(filepath.Ext(name))
	if len(ext) > 6 {
		ext = ""
	}
	path := filepath.Join(a.tmpDir, "clips", id+ext)
	f, err := os.Create(path)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, err = io.Copy(f, r.Body)
	f.Close()
	if err != nil {
		os.Remove(path)
		http.Error(w, "upload failed", 500)
		return
	}
	if a.ffmpeg == "" {
		os.Remove(path)
		http.Error(w, "FFmpeg not found", 500)
		return
	}
	clip, err := probe(a.ffmpeg, path)
	if err != nil {
		os.Remove(path)
		http.Error(w, name+" is not a video FFmpeg can read", 400)
		return
	}
	clip.ID, clip.Name, clip.Path = id, name, path
	a.mu.Lock()
	a.clips[id] = clip
	a.mu.Unlock()
	writeJSON(w, clip)
}

func (a *App) handleRemove(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	json.NewDecoder(r.Body).Decode(&req)
	a.mu.Lock()
	if c, ok := a.clips[req.ID]; ok {
		os.Remove(c.Path)
		delete(a.clips, req.ID)
	}
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	start := a.settings.OutputDir
	a.mu.Unlock()
	dir, err := pickFolder(start)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"path": dir})
}

func (a *App) handleOpenFolder(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	dir := a.settings.OutputDir
	a.mu.Unlock()
	os.MkdirAll(dir, 0o755)
	if err := openPath(dir); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func randID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func fatal(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print(msg)
	showError(msg)
	os.Exit(1)
}
