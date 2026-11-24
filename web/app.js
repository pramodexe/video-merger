"use strict";

const $ = (id) => document.getElementById(id);
const api = (path, body) =>
  fetch(path, {
    method: "POST",
    headers: { "X-App": "1", "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  }).then(async (r) => {
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  });

let settings = null;
let clips = []; // {key, id, name, duration, width, height, hasAudio, url, status}
let current = -1;
let playAll = false;
let exporting = false;

const stage = $("stage"), player = $("player"), wm = $("wm");

/* ---------------- helpers ---------------- */

function fmt(t) {
  t = Math.max(0, t || 0);
  const m = Math.floor(t / 60), s = t - m * 60;
  return String(m).padStart(2, "0") + ":" + s.toFixed(2).padStart(5, "0");
}
function hexToRgba(hex, a) {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${n >> 16 & 255},${n >> 8 & 255},${n & 255},${a})`;
}
const clamp01 = (v) => Math.max(0, Math.min(1, v));
const readyClips = () => clips.filter((c) => c.status === "ready");

/* ---------------- settings ---------------- */

let saveTimer;
function saveSettings() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    api("/api/settings", settings).then(() => {
      const n = $("savedNote");
      n.textContent = "Saved on this computer";
      n.classList.add("flash");
      setTimeout(() => n.classList.remove("flash"), 700);
    }).catch((e) => ($("savedNote").textContent = "Could not save: " + e.message));
  }, 350);
}

const POS = [[0, 0, "top left"], [0.5, 0, "top"], [1, 0, "top right"],
             [0, 0.5, "left"], [0.5, 0.5, "centre"], [1, 0.5, "right"],
             [0, 1, "bottom left"], [0.5, 1, "bottom"], [1, 1, "bottom right"]];

function buildGrid() {
  const g = $("grid9");
  POS.forEach(([fx, fy, name]) => {
    const b = document.createElement("button");
    b.type = "button";
    b.title = name;
    b.setAttribute("aria-label", name);
    b.style.placeItems = `${["start", "center", "end"][fy * 2]} ${["start", "center", "end"][fx * 2]}`;
    b.onclick = () => { settings.fx = fx; settings.fy = fy; applySettings(); saveSettings(); };
    g.appendChild(b);
  });
  ["#ffffff", "#000000", "#ffd21f", "#ff4b1f"].forEach((c) => {
    const b = document.createElement("button");
    b.type = "button";
    b.style.background = c;
    b.title = c;
    b.onclick = () => { settings.color = c; applySettings(); saveSettings(); };
    $("swatches").appendChild(b);
  });
}

function posName() {
  const p = POS.find(([x, y]) => Math.abs(x - settings.fx) < 1e-3 && Math.abs(y - settings.fy) < 1e-3);
  return p ? p[2] : "custom spot";
}

function applySettings() {
  const s = settings;
  $("wmText").value !== s.text && ($("wmText").value = s.text);
  $("wmSize").value = s.size;
  $("sizeOut").textContent = s.size + "%";
  $("wmMargin").value = s.margin;
  $("marginOut").textContent = s.margin + "%";
  $("wmColor").value = s.color;
  if (document.activeElement !== $("wmHex")) $("wmHex").value = s.color;
  $("wmOpacity").value = s.opacity;
  $("opOut").textContent = Math.round(s.opacity) + "%";
  if (document.activeElement !== $("outDir")) $("outDir").value = s.outputDir;
  [...$("grid9").children].forEach((b, i) => {
    b.classList.toggle("on", Math.abs(POS[i][0] - s.fx) < 1e-3 && Math.abs(POS[i][1] - s.fy) < 1e-3);
  });
  $("wmSummary").textContent = s.text ? `"${s.text}" · ${posName()} · ${Math.round(s.opacity)}%` : "off";
  layoutWM();
}

function bindInputs() {
  $("wmText").oninput = (e) => { settings.text = e.target.value; applySettings(); saveSettings(); };
  $("wmSize").oninput = (e) => { settings.size = +e.target.value; applySettings(); saveSettings(); };
  $("wmMargin").oninput = (e) => { settings.margin = +e.target.value; applySettings(); saveSettings(); };
  $("wmOpacity").oninput = (e) => { settings.opacity = +e.target.value; applySettings(); saveSettings(); };
  $("wmColor").oninput = (e) => { settings.color = e.target.value; applySettings(); saveSettings(); };
  $("wmHex").oninput = (e) => {
    let v = e.target.value.trim();
    if (!v.startsWith("#")) v = "#" + v;
    if (/^#[0-9a-f]{6}$/i.test(v)) { settings.color = v.toLowerCase(); applySettings(); saveSettings(); }
  };
  $("outDir").onchange = (e) => { settings.outputDir = e.target.value.trim(); saveSettings(); };
  $("wmPanel").ontoggle = () => {
    settings.panelOpen = $("wmPanel").open;
    saveSettings();
  };
}

/* ---------------- watermark preview ---------------- */

// Same maths as the FFmpeg drawtext filter in ffmpeg.go:
//   x = m + fx * (w - tw - 2m),  y = m + fy * (h - th - 2m)
let wmBox = { x: 0, y: 0, tw: 0, th: 0, m: 0 };

function layoutWM() {
  if (!settings) return;
  const sw = stage.clientWidth, sh = stage.clientHeight;
  wm.hidden = !settings.text;
  if (!settings.text) return;
  wm.textContent = settings.text;
  wm.style.fontSize = (settings.size / 100) * sh + "px";
  wm.style.color = hexToRgba(settings.color, settings.opacity / 100);
  const tw = wm.offsetWidth, th = wm.offsetHeight;
  const m = (settings.margin / 100) * sh;
  const x = m + settings.fx * (sw - tw - 2 * m);
  const y = m + settings.fy * (sh - th - 2 * m);
  wm.style.transform = `translate(${x}px, ${y}px)`;
  wmBox = { x, y, tw, th, m };
}

function bindDrag() {
  let off = null;
  wm.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    wm.setPointerCapture(e.pointerId);
    const r = stage.getBoundingClientRect();
    off = { x: e.clientX - r.left - wmBox.x, y: e.clientY - r.top - wmBox.y };
    wm.classList.add("drag");
  });
  wm.addEventListener("pointermove", (e) => {
    if (!off) return;
    const r = stage.getBoundingClientRect();
    const { tw, th, m } = wmBox;
    const spanX = r.width - tw - 2 * m, spanY = r.height - th - 2 * m;
    let fx = spanX > 0 ? clamp01((e.clientX - r.left - off.x - m) / spanX) : 0.5;
    let fy = spanY > 0 ? clamp01((e.clientY - r.top - off.y - m) / spanY) : 0.5;
    const snapX = Math.abs(fx - 0.5) < 0.03, snapY = Math.abs(fy - 0.5) < 0.03;
    if (snapX) fx = 0.5;
    if (snapY) fy = 0.5;
    $("guideX").classList.toggle("on", snapX);
    $("guideY").classList.toggle("on", snapY);
    settings.fx = +fx.toFixed(4);
    settings.fy = +fy.toFixed(4);
    applySettings();
  });
  const end = () => {
    if (!off) return;
    off = null;
    wm.classList.remove("drag");
    $("guideX").classList.remove("on");
    $("guideY").classList.remove("on");
    saveSettings();
  };
  wm.addEventListener("pointerup", end);
  wm.addEventListener("pointercancel", end);
  new ResizeObserver(layoutWM).observe(stage);
}

/* ---------------- clips ---------------- */

let keySeq = 0;
function addFiles(files) {
  const vids = [...files].filter((f) => f.type.startsWith("video/") || /\.(mp4|mov|mkv|webm|avi|m4v|3gp|ts)$/i.test(f.name));
  vids.sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
  for (const file of vids) {
    const c = { key: ++keySeq, name: file.name, url: URL.createObjectURL(file), status: "loading", pct: 0 };
    clips.push(c);
    upload(c, file);
  }
  render();
}

function upload(c, file) {
  const xhr = new XMLHttpRequest();
  xhr.open("POST", "/api/upload?name=" + encodeURIComponent(file.name));
  xhr.setRequestHeader("X-App", "1");
  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) { c.pct = Math.round((e.loaded / e.total) * 100); updateCard(c); }
  };
  xhr.onload = () => {
    if (xhr.status === 200) {
      Object.assign(c, JSON.parse(xhr.responseText), { status: "ready" });
      if (current < 0) select(clips.indexOf(c), false);
    } else {
      toast(xhr.responseText || "Could not add " + c.name);
      dropClip(c, false);
    }
    render();
  };
  xhr.onerror = () => { toast("Could not add " + c.name); dropClip(c, false); render(); };
  xhr.send(file);
}

function dropClip(c, tellServer = true) {
  const i = clips.indexOf(c);
  if (i < 0) return;
  clips.splice(i, 1);
  if (tellServer && c.id) api("/api/remove", { id: c.id }).catch(() => {});
  URL.revokeObjectURL(c.url);
  if (i === current) {
    current = -1;
    player.removeAttribute("src");
    player.load();
    if (clips.length) select(Math.min(i, clips.length - 1), false);
  } else if (i < current) current--;
}

function moveClip(from, to) {
  if (from === to) return;
  const activeKey = clips[current]?.key;
  const [c] = clips.splice(from, 1);
  clips.splice(to, 0, c);
  current = clips.findIndex((x) => x.key === activeKey);
  render();
}

function outputSize() {
  const first = readyClips()[0];
  return first ? [first.width, first.height] : null;
}

function render() {
  const track = $("track");
  const add = $("addBtn");
  const want = new Set(clips.map((c) => c.key));
  [...track.querySelectorAll(".clip")].forEach((el) => { if (!want.has(+el.dataset.key)) el.remove(); });
  const size = outputSize();

  clips.forEach((c, i) => {
    let el = track.querySelector(`.clip[data-key="${c.key}"]`);
    if (!el) el = makeCard(c);
    track.insertBefore(el, add);
    el.querySelector(".num").textContent = i + 1;
    el.classList.toggle("active", i === current);
    el.style.setProperty("--dur", c.duration || 5);
    updateCard(c, el, size);
  });

  const ready = readyClips();
  const total = ready.reduce((a, c) => a + c.duration, 0);
  $("clipCount").textContent = clips.length + (clips.length === 1 ? " clip" : " clips");
  $("totalTime").textContent = fmt(total);
  $("outSize").textContent = size ? `${size[0]}×${size[1]}` : "";
  stage.style.setProperty("--ar", size ? size[0] / size[1] : 16 / 9);
  stage.classList.toggle("has-clip", current >= 0);
  const busyLoading = clips.some((c) => c.status !== "ready");
  $("exportBtn").disabled = exporting || !ready.length || busyLoading || !hasFFmpeg;
  $("exportBtn").textContent = busyLoading ? "Adding clips…" : "Export video";
  requestAnimationFrame(layoutWM);
}

function makeCard(c) {
  const el = document.createElement("div");
  el.className = "clip";
  el.dataset.key = c.key;
  el.draggable = true;
  el.tabIndex = 0;
  el.innerHTML = `<div class="thumb"><video muted preload="metadata"></video><span class="num"></span>
    <button class="x" type="button" aria-label="Remove clip">×</button></div>
    <div class="cinfo"><div class="cname"></div><div class="ctags"></div></div>`;
  const v = el.querySelector("video");
  v.src = c.url;
  v.onloadedmetadata = () => { v.currentTime = Math.min(0.3, v.duration / 2 || 0); };
  el.querySelector(".cname").textContent = c.name;
  el.title = c.name;
  el.querySelector(".x").onclick = (e) => { e.stopPropagation(); dropClip(c); render(); };
  el.onclick = () => { playAll = false; select(clips.indexOf(c), false); };
  el.onkeydown = (e) => {
    const i = clips.indexOf(c);
    if (e.altKey && e.key === "ArrowLeft" && i > 0) { moveClip(i, i - 1); el.focus(); }
    if (e.altKey && e.key === "ArrowRight" && i < clips.length - 1) { moveClip(i, i + 1); el.focus(); }
    if (e.key === "Delete") { dropClip(c); render(); }
  };

  el.addEventListener("dragstart", (e) => {
    e.dataTransfer.setData("text/x-clip", String(c.key));
    e.dataTransfer.effectAllowed = "move";
    el.classList.add("dragging");
  });
  el.addEventListener("dragend", () => {
    el.classList.remove("dragging");
    document.querySelectorAll(".drop-before,.drop-after").forEach((x) => x.classList.remove("drop-before", "drop-after"));
  });
  el.addEventListener("dragover", (e) => {
    if (!e.dataTransfer.types.includes("text/x-clip")) return;
    e.preventDefault();
    const r = el.getBoundingClientRect();
    const after = e.clientX > r.left + r.width / 2;
    el.classList.toggle("drop-after", after);
    el.classList.toggle("drop-before", !after);
  });
  el.addEventListener("dragleave", () => el.classList.remove("drop-before", "drop-after"));
  el.addEventListener("drop", (e) => {
    if (!e.dataTransfer.types.includes("text/x-clip")) return;
    e.preventDefault();
    e.stopPropagation();
    const after = el.classList.contains("drop-after");
    el.classList.remove("drop-before", "drop-after");
    const from = clips.findIndex((x) => x.key === +e.dataTransfer.getData("text/x-clip"));
    let to = clips.indexOf(c) + (after ? 1 : 0);
    if (from < to) to--;
    moveClip(from, to);
  });
  return el;
}

function updateCard(c, el, size) {
  el = el || $("track").querySelector(`.clip[data-key="${c.key}"]`);
  if (!el) return;
  el.classList.toggle("loading", c.status !== "ready");
  el.querySelector(".thumb").dataset.pct = c.pct < 100 ? `adding ${c.pct}%` : "checking…";
  if (c.status !== "ready") return;
  size = size || outputSize();
  const tags = el.querySelector(".ctags");
  tags.innerHTML = "";
  const add = (text, cls, tip) => {
    const s = document.createElement("span");
    s.textContent = text;
    if (cls) s.className = cls;
    if (tip) s.title = tip;
    tags.appendChild(s);
  };
  add(fmt(c.duration), "tc");
  if (!c.hasAudio) add("no sound", "", "Silent audio will be added");
  if (size && (c.width !== size[0] || c.height !== size[1]))
    add(`${c.width}×${c.height}`, "warn", `Will be fitted into ${size[0]}×${size[1]} with black bars`);
}

/* ---------------- player ---------------- */

function select(i, autoplay) {
  if (i < 0 || i >= clips.length) return;
  current = i;
  player.src = clips[i].url;
  $("nowName").textContent = `${i + 1}. ${clips[i].name}`;
  if (autoplay) player.play().catch(() => {});
  render();
}

function bindPlayer() {
  $("playBtn").onclick = () => {
    if (current < 0) return;
    player.paused ? player.play() : player.pause();
  };
  $("playAllBtn").onclick = () => {
    if (!clips.length) return;
    playAll = !playAll;
    $("playAllBtn").classList.toggle("on", playAll);
    if (playAll) select(0, true);
    else player.pause();
  };
  player.onplay = () => $("playBtn").classList.add("playing");
  player.onpause = () => $("playBtn").classList.remove("playing");
  player.onended = () => {
    if (playAll && current < clips.length - 1) select(current + 1, true);
    else { playAll = false; $("playAllBtn").classList.remove("on"); }
  };
  let seeking = false;
  $("scrub").oninput = (e) => {
    seeking = true;
    if (player.duration) player.currentTime = (e.target.value / 1000) * player.duration;
  };
  $("scrub").onchange = () => (seeking = false);
  const tick = () => {
    if (player.duration && !seeking) $("scrub").value = (player.currentTime / player.duration) * 1000;
    $("clipTime").textContent = `${fmt(player.currentTime)} / ${fmt(player.duration)}`;
    requestAnimationFrame(tick);
  };
  tick();
  document.addEventListener("keydown", (e) => {
    if (e.code === "Space" && !/INPUT|BUTTON|SUMMARY/.test(document.activeElement.tagName)) {
      e.preventDefault();
      $("playBtn").click();
    }
  });
}

/* ---------------- timeline resize ---------------- */

const TL_MIN = 120;
const tlMax = () => Math.max(TL_MIN, Math.round(window.innerHeight * 0.65));
function setTimelineH(h) {
  h = Math.round(Math.max(TL_MIN, Math.min(tlMax(), h)));
  document.documentElement.style.setProperty("--tl-h", h + "px");
  return h;
}

function bindResize() {
  const bar = $("tlResize");
  let start = null;
  bar.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    bar.setPointerCapture(e.pointerId);
    start = { y: e.clientY, h: document.querySelector(".timeline").offsetHeight };
    bar.classList.add("drag");
    document.body.classList.add("resizing");
  });
  bar.addEventListener("pointermove", (e) => {
    if (!start) return;
    settings.timelineH = setTimelineH(start.h + (start.y - e.clientY));
  });
  const end = () => {
    if (!start) return;
    start = null;
    bar.classList.remove("drag");
    document.body.classList.remove("resizing");
    saveSettings();
  };
  bar.addEventListener("pointerup", end);
  bar.addEventListener("pointercancel", end);
  bar.addEventListener("dblclick", () => { settings.timelineH = setTimelineH(170); saveSettings(); });
  window.addEventListener("resize", () => settings && settings.timelineH && setTimelineH(settings.timelineH));
}

/* ---------------- drop files anywhere ---------------- */

function bindDrop() {
  const isFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes("Files");
  document.addEventListener("dragover", (e) => {
    if (!isFiles(e)) return;
    e.preventDefault();
    document.body.classList.add("dragover");
  });
  document.addEventListener("dragleave", (e) => {
    if (!e.relatedTarget) document.body.classList.remove("dragover");
  });
  document.addEventListener("drop", (e) => {
    document.body.classList.remove("dragover");
    if (!isFiles(e)) return;
    e.preventDefault();
    addFiles(e.dataTransfer.files);
  });
  const pick = () => $("fileInput").click();
  $("addBtn").onclick = pick;
  $("pickBtn").onclick = pick;
  $("fileInput").onchange = (e) => { addFiles(e.target.files); e.target.value = ""; };
}

/* ---------------- export ---------------- */

function setExporting(on) {
  exporting = on;
  document.body.classList.toggle("busy", on);
  $("progress").hidden = !on;
  render();
}

function showResult(kind, nodeFn) {
  const r = $("result");
  r.hidden = false;
  r.className = "result " + kind;
  r.innerHTML = "";
  nodeFn(r);
}

function bindExport() {
  $("exportBtn").onclick = async () => {
    $("result").hidden = true;
    settings.outputDir = $("outDir").value.trim();
    try {
      player.pause();
      await api("/api/export", { ids: clips.map((c) => c.id), settings });
      $("pfill").style.width = "0%";
      $("pct").textContent = "0%";
      setExporting(true);
    } catch (e) {
      showResult("err", (r) => (r.textContent = e.message));
    }
  };
  $("cancelBtn").onclick = () => api("/api/cancel").catch(() => {});
  $("browseBtn").onclick = async () => {
    const r = await api("/api/pick-folder").catch((e) => ({ error: e.message }));
    if (r.error) toast(r.error);
    else if (r.path) { settings.outputDir = r.path; applySettings(); saveSettings(); }
  };
  $("openBtn").onclick = () => {
    settings.outputDir = $("outDir").value.trim();
    api("/api/settings", settings).then(() => api("/api/open-folder")).catch((e) => toast(e.message));
  };
}

function onEvent(ev) {
  if (ev.type === "progress") {
    if (!exporting) setExporting(true);
    $("pfill").style.width = ev.pct + "%";
    $("pct").textContent = ev.pct + "%";
  } else if (ev.type === "done") {
    setExporting(false);
    const name = ev.path.split(/[\\/]/).pop();
    showResult("ok", (r) => {
      const b = document.createElement("b");
      b.textContent = "Saved " + name;
      const p = document.createElement("div");
      p.className = "dim";
      p.textContent = ev.path;
      r.append(b, p);
    });
  } else if (ev.type === "error") {
    setExporting(false);
    showResult("err", (r) => (r.textContent = "Export failed:\n" + ev.msg));
  } else if (ev.type === "cancelled") {
    setExporting(false);
    showResult("", (r) => (r.textContent = "Export cancelled."));
  }
}

/* ---------------- connection ---------------- */

let es = null;
// Close the stream when the page is left, even if the browser keeps the page
// in its back/forward cache, so the app can shut down when no tab is open.
window.addEventListener("pagehide", () => { if (es) { es.close(); es = null; } });
window.addEventListener("pageshow", (e) => { if (e.persisted && !es) connect(); });

function connect() {
  es = new EventSource("/api/events");
  es.onmessage = (m) => onEvent(JSON.parse(m.data));
  es.onerror = () => {
    fetch("/api/state").catch(() => {
      es.close();
      $("closed").hidden = false;
    });
  };
}

let toastTimer;
function toast(msg) {
  let t = $("toast");
  if (!t) {
    t = document.createElement("div");
    t.id = "toast";
    Object.assign(t.style, {
      position: "fixed", left: "50%", bottom: "24px", transform: "translateX(-50%)",
      background: "#4a1d10", color: "#ffd9cc", padding: "10px 16px", borderRadius: "4px",
      border: "1px solid #7a2c16", zIndex: 70, maxWidth: "80vw",
    });
    document.body.appendChild(t);
  }
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), 5000);
}

/* ---------------- start ---------------- */

let hasFFmpeg = true;
(async function start() {
  buildGrid();
  bindInputs();
  bindDrag();
  bindPlayer();
  bindDrop();
  bindExport();
  bindResize();
  const st = await fetch("/api/state").then((r) => r.json());
  settings = st.settings;
  hasFFmpeg = st.hasFFmpeg;
  $("noFFmpeg").hidden = hasFFmpeg;
  $("wmPanel").open = settings.panelOpen;
  if (settings.timelineH) setTimelineH(settings.timelineH);
  await document.fonts.load('700 20px "WM"');
  applySettings();
  render();
  if (st.exporting) setExporting(true);
  connect();
})();
