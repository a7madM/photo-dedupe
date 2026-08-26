// Package webui serves a local, browser-based gallery: pick a
// directory on the page, scan it, then review each group's winner and
// losers side by side and trigger apply or restore, without touching
// a terminal. Nothing here talks to the network beyond whatever
// loopback address the caller binds it to.
package webui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a7madM/photo-dedupe/internal/apply"
	"github.com/a7madM/photo-dedupe/internal/imagemetrics"
	"github.com/a7madM/photo-dedupe/internal/plan"
	"github.com/a7madM/photo-dedupe/internal/scan"
)

const (
	defaultGap        = "60s"
	defaultSimilarity = "8"
	defaultBlur       = "5e6"
)

// Server is an http.Handler serving a directory-picker + gallery view.
// It starts with no plan loaded; a scan submitted through the page
// populates one.
type Server struct {
	mux *http.ServeMux

	mu          sync.Mutex
	hasPlan     bool
	p           plan.Plan
	allowed     map[string]bool
	warnings    []scan.Warning
	bannerText  string
	bannerError bool
	gapStr      string
	simStr      string
	blurStr     string

	// scanning tracks a scan running in the background so handleIndex
	// can render its progress and handleScan can refuse to start a
	// second one on top of it. scanDirectory/scanCurrent/scanTotal/
	// scanPath are only meaningful while scanning is true.
	scanning      bool
	scanDirectory string
	scanCurrent   int
	scanTotal     int
	scanPath      string
}

// New builds a Server with no plan loaded yet.
func New() *Server {
	s := &Server{
		allowed: map[string]bool{},
		gapStr:  defaultGap,
		simStr:  defaultSimilarity,
		blurStr: defaultBlur,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/scan", s.handleScan)
	mux.HandleFunc("/scan/progress", s.handleScanProgress)
	mux.HandleFunc("/browse", s.handleBrowse)
	mux.HandleFunc("/image", s.handleImage)
	mux.HandleFunc("/apply", s.handleApply)
	mux.HandleFunc("/restore", s.handleRestore)
	s.mux = mux

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func allowedPaths(p plan.Plan) map[string]bool {
	allowed := make(map[string]bool)
	for _, g := range p.Groups {
		allowed[g.Winner.Path] = true
		for _, l := range g.Losers {
			allowed[l.Path] = true
		}
	}
	return allowed
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	s.mu.Lock()
	root := s.p.Root
	if s.scanning {
		root = s.scanDirectory
	}
	data := pageData{
		HasPlan:     s.hasPlan,
		Root:        root,
		Scanning:    s.scanning,
		GapStr:      s.gapStr,
		SimStr:      s.simStr,
		BlurStr:     s.blurStr,
		BannerText:  s.bannerText,
		BannerError: s.bannerError,
		Warnings:    s.warnings,
	}
	if s.hasPlan {
		data.Groups = make([]groupView, len(s.p.Groups))
		for i, g := range s.p.Groups {
			losers := make([]imageView, len(g.Losers))
			for j, l := range g.Losers {
				losers[j] = toImageView(l)
			}
			data.Groups[i] = groupView{ID: g.ID, Winner: toImageView(g.Winner), Losers: losers}
		}
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleScan runs a scan against the directory submitted in the form
// and, on success, replaces the server's current plan with the
// result. It never fails the request outright on a bad or unscanable
// directory — it redirects back to "/" with an error banner instead,
// so a typo doesn't crash the page.
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	dir := strings.TrimSpace(r.FormValue("directory"))
	gapStr := formValueOr(r, "gap", defaultGap)
	simStr := formValueOr(r, "similarity", defaultSimilarity)
	blurStr := formValueOr(r, "blur", defaultBlur)

	if dir == "" {
		s.setBanner("choose a directory to scan", true)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	gap, err := time.ParseDuration(gapStr)
	if err != nil {
		s.setBanner(fmt.Sprintf("invalid gap %q: %v", gapStr, err), true)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	sim, err := strconv.Atoi(simStr)
	if err != nil {
		s.setBanner(fmt.Sprintf("invalid similarity %q: %v", simStr, err), true)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	blur, err := strconv.ParseFloat(blurStr, 64)
	if err != nil {
		s.setBanner(fmt.Sprintf("invalid blur %q: %v", blurStr, err), true)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	root, err := filepath.Abs(dir)
	if err != nil {
		s.setBanner(err.Error(), true)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	s.mu.Lock()
	if s.scanning {
		s.mu.Unlock()
		s.setBanner("a scan is already running — wait for it to finish", true)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.scanning = true
	s.scanDirectory = root
	s.scanCurrent = 0
	s.scanTotal = 0
	s.scanPath = ""
	s.bannerText = ""
	s.bannerError = false
	s.mu.Unlock()

	// Scanning a real photo library means decoding every image on
	// disk, which can run for minutes — this runs in the background
	// so the page can render immediately and poll /scan/progress for
	// live updates instead of leaving the browser hanging with no
	// feedback until the whole thing finishes.
	go func() {
		p, warnings, err := scan.Run(scan.Options{
			Root:                root,
			GapThreshold:        gap,
			SimilarityThreshold: sim,
			BlurThreshold:       blur,
			Progress: func(current, total int, path string) {
				s.mu.Lock()
				s.scanCurrent = current
				s.scanTotal = total
				s.scanPath = path
				s.mu.Unlock()
			},
		})

		s.mu.Lock()
		defer s.mu.Unlock()
		s.scanning = false
		if err != nil {
			s.bannerText = fmt.Sprintf("scanning %s: %v", root, err)
			s.bannerError = true
			return
		}

		if f, err := os.Create(filepath.Join(root, ".dedupe-plan.json")); err == nil {
			plan.Write(f, p)
			f.Close()
		}

		s.hasPlan = true
		s.p = p
		s.allowed = allowedPaths(p)
		s.warnings = warnings
		s.gapStr, s.simStr, s.blurStr = gapStr, simStr, blurStr
		s.bannerText = fmt.Sprintf("scanned %s: %d group(s) found", root, len(p.Groups))
		s.bannerError = false
	}()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// scanProgressView is served by handleScanProgress and polled by the
// page while a scan runs in the background.
type scanProgressView struct {
	Active    bool   `json:"active"`
	Current   int    `json:"current"`
	Total     int    `json:"total"`
	Path      string `json:"path"`
	Directory string `json:"directory"`
}

func (s *Server) handleScanProgress(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	resp := scanProgressView{
		Active:    s.scanning,
		Current:   s.scanCurrent,
		Total:     s.scanTotal,
		Path:      filepath.Base(s.scanPath),
		Directory: s.scanDirectory,
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func formValueOr(r *http.Request, key, fallback string) string {
	if v := strings.TrimSpace(r.FormValue(key)); v != "" {
		return v
	}
	return fallback
}

// browseEntry is one directory (either a listing row or a shortcut)
// offered to the folder-picker UI.
type browseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// browseResponse is served by handleBrowse: the resolved directory,
// its subdirectories, its parent (for "up" navigation), and a fixed
// set of shortcut jump points. Error is set instead of Entries when
// the directory can't be listed (permissions, since deleted, etc.) —
// the picker stays open and navigable rather than erroring out.
type browseResponse struct {
	Path      string        `json:"path"`
	Parent    string        `json:"parent,omitempty"`
	Entries   []browseEntry `json:"entries"`
	Shortcuts []browseEntry `json:"shortcuts"`
	Error     string        `json:"error,omitempty"`
}

// handleBrowse lists the subdirectories of a path so the page's
// folder picker can walk the filesystem without the browser ever
// needing to know a real absolute path itself — a webkitdirectory
// file input can't hand back one, and pasting is what this replaces.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		} else {
			path = string(filepath.Separator)
		}
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	path = filepath.Clean(path)

	resp := browseResponse{Path: path, Shortcuts: shortcuts()}
	if path != string(filepath.Separator) {
		resp.Parent = filepath.Dir(path)
	}

	info, err := os.Stat(path)
	switch {
	case err != nil:
		resp.Error = err.Error()
	case !info.IsDir():
		resp.Error = path + " is not a directory"
	default:
		dirEntries, err := os.ReadDir(path)
		if err != nil {
			resp.Error = err.Error()
			break
		}
		resp.Entries = []browseEntry{}
		for _, e := range dirEntries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			resp.Entries = append(resp.Entries, browseEntry{Name: e.Name(), Path: filepath.Join(path, e.Name())})
		}
		sort.Slice(resp.Entries, func(i, j int) bool {
			return strings.ToLower(resp.Entries[i].Name) < strings.ToLower(resp.Entries[j].Name)
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// shortcuts lists the fixed jump points offered at the top of the
// folder picker, filtered to ones that actually exist on this
// machine.
func shortcuts() []browseEntry {
	var out []browseEntry
	add := func(name, path string) {
		if path == "" {
			return
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			out = append(out, browseEntry{Name: name, Path: path})
		}
	}

	home, _ := os.UserHomeDir()
	add("Home", home)
	if home != "" {
		add("Desktop", filepath.Join(home, "Desktop"))
		add("Documents", filepath.Join(home, "Documents"))
		add("Pictures", filepath.Join(home, "Pictures"))
		add("Downloads", filepath.Join(home, "Downloads"))
	}
	if runtime.GOOS == "darwin" {
		add("Volumes", "/Volumes")
	}
	return out
}

// handleImage serves the raw bytes of an image referenced by the
// current plan. JPEG/PNG are streamed as-is; HEIC/HEIF is decoded and
// re-encoded as JPEG on the fly, since most browsers other than
// Safari can't render HEIC directly.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")

	s.mu.Lock()
	allowed := s.allowed[path]
	s.mu.Unlock()
	if !allowed {
		http.NotFound(w, r)
		return
	}

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".heic" || ext == ".heif" {
		img, err := imagemetrics.Decode(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		if err := jpeg.Encode(w, img, &jpeg.Options{Quality: 85}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	ct := "image/jpeg"
	if ext == ".png" {
		ct = "image/png"
	}
	w.Header().Set("Content-Type", ct)
	io.Copy(w, f)
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	hasPlan, p, scanning := s.hasPlan, s.p, s.scanning
	s.mu.Unlock()
	if scanning {
		http.Error(w, "a scan is in progress — wait for it to finish", http.StatusConflict)
		return
	}
	if !hasPlan {
		http.Error(w, "no plan loaded — scan a directory first", http.StatusBadRequest)
		return
	}

	results, err := apply.Apply(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.recordResult("apply", results)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	hasPlan, p, scanning := s.hasPlan, s.p, s.scanning
	s.mu.Unlock()
	if scanning {
		http.Error(w, "a scan is in progress — wait for it to finish", http.StatusConflict)
		return
	}
	if !hasPlan {
		http.Error(w, "no plan loaded — scan a directory first", http.StatusBadRequest)
		return
	}

	results, err := apply.Restore(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.recordResult("restore", results)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) recordResult(action string, results []apply.Result) {
	var winners, losers, drift, missing int
	for _, r := range results {
		switch {
		case r.Outcome == apply.OutcomeMoved && r.Role == apply.RoleWinner:
			winners++
		case r.Outcome == apply.OutcomeMoved && r.Role == apply.RoleLoser:
			losers++
		case r.Outcome == apply.OutcomeSkippedDrift:
			drift++
		case r.Outcome == apply.OutcomeSkippedMissing:
			missing++
		}
	}
	text := fmt.Sprintf("%s complete: %d winners moved, %d losers moved, %d skipped (drift), %d skipped (missing)",
		action, winners, losers, drift, missing)
	s.setBanner(text, false)
}

func (s *Server) setBanner(text string, isError bool) {
	s.mu.Lock()
	s.bannerText = text
	s.bannerError = isError
	s.mu.Unlock()
}

type imageView struct {
	Base      string
	URL       string
	Width     int
	Height    int
	SizeMB    string
	Sharpness float64
}

func toImageView(r plan.FileRecord) imageView {
	return imageView{
		Base:      filepath.Base(r.Path),
		URL:       "/image?" + url.Values{"path": {r.Path}}.Encode(),
		Width:     r.Width,
		Height:    r.Height,
		SizeMB:    fmt.Sprintf("%.1f MB", float64(r.SizeBytes)/1e6),
		Sharpness: r.Sharpness,
	}
}

type groupView struct {
	ID     int
	Winner imageView
	Losers []imageView
}

type pageData struct {
	HasPlan     bool
	Root        string
	Scanning    bool
	GapStr      string
	SimStr      string
	BlurStr     string
	BannerText  string
	BannerError bool
	Warnings    []scan.Warning
	Groups      []groupView
}

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

const indexHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>photo-dedupe</title>
<style>
  /* Committed to a single dark "darkroom" surround rather than a light/dark
     toggle: this page exists to judge photos against each other, and a warm,
     near-black ground with no competing chrome is what makes that judgment
     easier — the same reason photo culling tools default to a dark UI. */
  :root {
    --bg: #14100d;
    --bg-raised: #1d1712;
    --bg-card: #211a14;
    --rule: #3a2f24;
    --paper: #f0e6d6;
    --paper-dim: #b9ab97;
    --muted: #8a7c6a;
    --safelight: #e2481f;
    --safelight-dim: #8a2c15;
    --gold: #cf9f33;
    --gold-dim: #8a6f2b;
    --serif: Georgia, "Iowan Old Style", "Palatino Linotype", "Times New Roman", serif;
    --mono: ui-monospace, "SF Mono", "Cascadia Mono", "Roboto Mono", Menlo, Consolas, monospace;
    --shadow: 0 20px 50px -18px rgba(0,0,0,.75);
  }

  * { box-sizing: border-box; }

  html, body { background: var(--bg); }

  body {
    position: relative;
    font-family: var(--serif);
    color: var(--paper);
    margin: 0;
    padding: 2.5rem 1.5rem 5rem;
    min-height: 100vh;
    overflow-x: hidden;
  }
  body.modal-open { overflow: hidden; }

  body::before {
    content: "";
    position: fixed; inset: 0;
    background:
      radial-gradient(ellipse at 50% -10%, rgba(226,72,31,.10), transparent 55%),
      radial-gradient(ellipse at 50% 110%, rgba(0,0,0,.55), transparent 65%);
    pointer-events: none;
    z-index: 0;
  }
  .grain {
    position: fixed; inset: 0;
    opacity: .05;
    mix-blend-mode: overlay;
    pointer-events: none;
    z-index: 2;
    background-image: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg'><filter id='n'><feTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='2' stitchTiles='stitch'/></filter><rect width='100%25' height='100%25' filter='url(%23n)'/></svg>");
  }
  .filmstrip-edge {
    position: fixed; top: 0; bottom: 0; width: 16px;
    background-image: radial-gradient(circle at 8px 9px, var(--bg) 4px, transparent 4.5px);
    background-size: 16px 20px;
    background-repeat: repeat-y;
    background-color: #241d16;
    opacity: .55;
    z-index: 1;
    pointer-events: none;
  }
  .filmstrip-edge.left { left: 0; }
  .filmstrip-edge.right { right: 0; }

  .page { position: relative; z-index: 3; max-width: 76rem; margin: 0 auto; }

  .leader {
    height: 6px;
    margin: -2.5rem -1.5rem 2rem;
    background-image: repeating-linear-gradient(135deg, var(--gold-dim) 0 14px, transparent 14px 28px);
    opacity: .55;
  }

  .site-header { margin-bottom: 2rem; }
  .site-header h1 {
    font-size: 2.3rem;
    margin: 0 0 .3rem;
    letter-spacing: .01em;
    animation: safelight-pulse 7s ease-in-out infinite;
  }
  .site-header .subtitle {
    font-family: var(--mono);
    text-transform: uppercase;
    letter-spacing: .3em;
    font-size: .68rem;
    color: var(--gold);
    margin-left: .1em;
  }
  @keyframes safelight-pulse {
    0%, 100% { text-shadow: 0 0 18px rgba(226,72,31,0); }
    50% { text-shadow: 0 0 18px rgba(226,72,31,.35); }
  }

  .banner {
    font-family: var(--mono);
    font-size: .85rem;
    background: var(--bg-card);
    border: 1px solid var(--rule);
    border-left: 4px solid var(--gold);
    border-radius: 4px;
    padding: .8rem 1.1rem;
    margin-bottom: 1.75rem;
    color: var(--paper-dim);
    animation: tape-down .38s cubic-bezier(.2,.9,.3,1.2) both;
  }
  .banner.error { border-left-color: var(--safelight); color: var(--paper); background: rgba(226,72,31,.08); }
  @keyframes tape-down {
    from { transform: translateY(-12px); opacity: 0; }
    to { transform: translateY(0); opacity: 1; }
  }

  .ticket {
    background: var(--bg-raised);
    border: 1px solid var(--rule);
    border-radius: 12px;
    padding: 1.4rem 1.5rem;
    margin-bottom: 2rem;
    box-shadow: var(--shadow);
  }
  .field label, .dial label {
    display: block;
    font-family: var(--mono);
    text-transform: uppercase;
    letter-spacing: .18em;
    font-size: .68rem;
    color: var(--gold);
    margin-bottom: .4rem;
  }
  .field-control { display: flex; gap: .6rem; }
  #directory-field {
    flex: 1;
    min-width: 0;
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 8px;
    color: var(--paper);
    font-family: var(--mono);
    font-size: .92rem;
    padding: .65rem .8rem;
    cursor: pointer;
  }
  #directory-field::placeholder { color: var(--muted); }
  #directory-field:focus { outline: 2px solid var(--safelight-dim); outline-offset: 1px; }

  .btn-browse {
    display: inline-flex; align-items: center; gap: .45rem;
    background: var(--bg-card);
    border: 1px solid var(--rule);
    color: var(--gold);
    padding: 0 1rem;
    border-radius: 8px;
    font-family: var(--mono);
    text-transform: uppercase;
    letter-spacing: .06em;
    font-size: .78rem;
    cursor: pointer;
    transition: border-color .15s ease, color .15s ease, background .15s ease;
  }
  .btn-browse:hover { border-color: var(--gold); color: var(--paper); background: var(--bg-raised); }

  .dials-row {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 1.75rem;
    margin-top: 1.25rem;
  }
  .dial { min-width: 9rem; }
  .dial-control { display: flex; align-items: center; gap: .6rem; }
  .dial input[type="range"] {
    flex: 1;
    accent-color: var(--safelight);
    background: transparent;
  }
  .dial input[type="text"] {
    width: 4.5rem;
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 6px;
    color: var(--paper);
    font-family: var(--mono);
    font-size: .85rem;
    padding: .35rem .5rem;
  }
  .dial input[type="text"]:focus { outline: 2px solid var(--safelight-dim); outline-offset: 1px; }

  .shutter-btn {
    margin-left: auto;
    width: 72px; height: 72px;
    border-radius: 50%;
    background: radial-gradient(circle at 35% 30%, #ff6a3f, var(--safelight) 55%, var(--safelight-dim) 100%);
    border: 3px solid var(--gold-dim);
    color: var(--paper);
    font-family: var(--mono);
    font-weight: 700;
    letter-spacing: .1em;
    font-size: .72rem;
    cursor: pointer;
    box-shadow: 0 8px 20px rgba(226,72,31,.35);
    transition: transform .12s ease, box-shadow .12s ease, filter .15s ease;
  }
  .shutter-btn:hover { filter: brightness(1.08); }
  .shutter-btn:active { transform: scale(.93); box-shadow: 0 4px 10px rgba(226,72,31,.3); }
  .shutter-btn:disabled, .btn-browse:disabled, .btn-stamp:disabled, .btn-ghost:disabled {
    opacity: .4;
    cursor: default;
    filter: none;
    transform: none !important;
  }
  #directory-field:disabled { opacity: .6; cursor: default; }

  .develop-panel { margin-top: 1.3rem; padding-top: 1.1rem; border-top: 1px dashed var(--rule); }
  .develop-panel[hidden] { display: none; }
  .develop-head {
    display: flex; align-items: baseline; gap: .55rem;
    font-family: var(--mono);
    margin-bottom: .55rem;
  }
  .develop-dot {
    width: 8px; height: 8px; border-radius: 50%;
    background: var(--safelight);
    box-shadow: 0 0 8px rgba(226,72,31,.7);
    animation: safelight-pulse-dot 1.1s ease-in-out infinite;
  }
  @keyframes safelight-pulse-dot {
    0%, 100% { opacity: .4; }
    50% { opacity: 1; }
  }
  .develop-label { color: var(--paper); font-size: .82rem; text-transform: uppercase; letter-spacing: .08em; }
  .develop-count { color: var(--gold); font-size: .8rem; margin-left: auto; }
  .develop-track {
    height: 10px;
    border-radius: 999px;
    background: var(--bg);
    border: 1px solid var(--rule);
    overflow: hidden;
    position: relative;
  }
  .develop-fill {
    height: 100%;
    width: 0%;
    background: linear-gradient(90deg, var(--gold-dim), var(--safelight));
    border-radius: 999px;
    transition: width .3s ease;
  }
  .develop-fill.indeterminate {
    position: absolute; inset: 0 auto 0 0;
    width: 40% !important;
    background-image: repeating-linear-gradient(135deg, var(--safelight) 0 10px, var(--gold-dim) 10px 20px);
    animation: develop-sweep 1.1s linear infinite;
  }
  @keyframes develop-sweep {
    0% { left: -40%; }
    100% { left: 100%; }
  }
  .develop-path {
    margin-top: .5rem;
    font-family: var(--mono);
    font-size: .75rem;
    color: var(--muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .actions { margin-bottom: 1rem; display: flex; gap: .9rem; flex-wrap: wrap; }
  .actions form { display: inline-block; }

  .btn-stamp {
    background: var(--safelight);
    color: var(--paper);
    border: none;
    border-radius: 9px;
    padding: .85rem 1.5rem;
    font-family: var(--mono);
    text-transform: uppercase;
    letter-spacing: .04em;
    font-weight: 700;
    font-size: .82rem;
    cursor: pointer;
    transform: rotate(-1deg);
    box-shadow: 0 10px 22px rgba(226,72,31,.32);
    transition: transform .15s ease, box-shadow .15s ease;
  }
  .btn-stamp:hover { transform: rotate(0deg) translateY(-2px); }
  .btn-stamp:active { transform: rotate(0deg) translateY(0); box-shadow: 0 4px 10px rgba(226,72,31,.3); }

  .btn-ghost {
    background: transparent;
    border: 1px solid var(--rule);
    color: var(--paper-dim);
    border-radius: 9px;
    padding: .85rem 1.4rem;
    font-family: var(--mono);
    text-transform: uppercase;
    letter-spacing: .04em;
    font-size: .82rem;
    cursor: pointer;
    transition: border-color .15s ease, color .15s ease;
  }
  .btn-ghost:hover { border-color: var(--gold); color: var(--gold); }

  .warnings { font-family: var(--mono); font-size: .8rem; color: var(--muted); margin-bottom: 1.75rem; }
  .empty { color: var(--muted); font-style: italic; }

  .group {
    background: var(--bg-raised);
    border: 1px solid var(--rule);
    border-radius: 14px;
    margin-bottom: 2rem;
    box-shadow: var(--shadow);
    overflow: hidden;
    opacity: 0;
    animation: rise-in .5s ease calc(var(--i, 0) * 70ms) both;
  }
  @keyframes rise-in {
    from { opacity: 0; transform: translateY(16px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .group-head {
    display: flex; justify-content: space-between; align-items: baseline;
    padding: .9rem 1.4rem 0;
    font-family: var(--mono);
  }
  .roll-no { color: var(--paper); font-size: .9rem; letter-spacing: .04em; }
  .frame-count { color: var(--muted); font-size: .72rem; text-transform: uppercase; letter-spacing: .1em; }

  .sprockets {
    height: 13px;
    margin-top: .7rem;
    background-color: #150f0a;
    background-image: radial-gradient(circle at 9px 6.5px, var(--bg-raised) 3.6px, transparent 4px);
    background-size: 18px 13px;
    background-repeat: repeat-x;
  }

  .filmstrip {
    display: flex;
    flex-wrap: wrap;
    gap: 1.5rem;
    padding: 1.5rem;
    background-color: #150f0a;
  }

  .frame { width: 210px; }
  .frame:nth-child(3n) .frame-photo { transform: rotate(-.6deg); }
  .frame:nth-child(3n+1) .frame-photo { transform: rotate(.9deg); }
  .frame:nth-child(3n+2) .frame-photo { transform: rotate(-1.2deg); }

  .frame-photo {
    position: relative;
    background: #0c0906;
    border: 6px solid var(--paper);
    border-radius: 2px;
    box-shadow: 0 10px 24px rgba(0,0,0,.55);
    transition: transform .25s ease, box-shadow .25s ease, z-index 0s;
  }
  .frame-photo:hover {
    transform: rotate(0deg) scale(1.04) !important;
    box-shadow: 0 18px 34px rgba(0,0,0,.65);
    z-index: 4;
  }
  .frame-photo img {
    display: block;
    width: 100%;
    height: 210px;
    object-fit: cover;
  }

  .mark { position: absolute; inset: 0; width: 100%; height: 100%; pointer-events: none; }

  .stamp {
    position: absolute; top: 8px; left: 8px;
    font-family: var(--mono);
    text-transform: uppercase;
    letter-spacing: .18em;
    font-size: .62rem;
    font-weight: 700;
    padding: .15rem .5rem;
    border: 2px solid var(--safelight);
    color: var(--safelight);
    background: rgba(226,72,31,.1);
    transform: rotate(-8deg);
    pointer-events: none;
  }

  .frame-caption {
    font-family: var(--mono);
    font-size: .78rem;
    color: var(--paper-dim);
    margin-top: .6rem;
    line-height: 1.4;
    word-break: break-all;
  }
  .exif { color: var(--muted); font-size: .7rem; }

  .browse-backdrop {
    position: fixed; inset: 0;
    background: rgba(8,6,4,.72);
    backdrop-filter: blur(2px);
    z-index: 500;
  }
  .browse-backdrop[hidden] { display: none; }

  .browse-modal { position: fixed; inset: 0; z-index: 501; padding: 2rem; }
  .browse-modal[hidden] { display: none; }
  .browse-modal:not([hidden]) { display: flex; align-items: center; justify-content: center; }

  .browse-panel {
    width: min(580px, 92vw);
    max-height: 82vh;
    display: flex;
    flex-direction: column;
    background: var(--bg-raised);
    border: 1px solid var(--rule);
    border-radius: 14px;
    box-shadow: var(--shadow);
    overflow: hidden;
    animation: flicker-in .3s ease both;
  }
  @keyframes flicker-in {
    0% { opacity: 0; }
    8% { opacity: 1; }
    16% { opacity: .25; }
    26% { opacity: 1; }
    100% { opacity: 1; }
  }

  .browse-crumbs {
    display: flex; flex-wrap: wrap; gap: .25rem;
    padding: 1rem 1rem 0;
  }
  .crumb {
    font-family: var(--mono);
    font-size: .78rem;
    background: var(--bg-card);
    border: 1px solid var(--rule);
    border-bottom: none;
    color: var(--paper-dim);
    padding: .3rem .65rem;
    border-radius: 6px 6px 0 0;
    cursor: pointer;
  }
  .crumb.current { background: rgba(226,72,31,.14); border-color: var(--safelight-dim); color: var(--paper); }
  .crumb:hover { color: var(--paper); }

  .browse-shortcuts {
    display: flex; flex-wrap: wrap; gap: .5rem;
    padding: .85rem 1rem;
    border-bottom: 1px solid var(--rule);
  }
  .shortcut-pill {
    font-family: var(--mono);
    text-transform: uppercase;
    letter-spacing: .06em;
    font-size: .68rem;
    background: transparent;
    border: 1px solid var(--rule);
    color: var(--gold);
    border-radius: 999px;
    padding: .3rem .75rem;
    cursor: pointer;
    transition: background .15s ease, color .15s ease;
  }
  .shortcut-pill:hover { background: var(--gold); color: var(--bg); }

  .browse-error {
    margin: .75rem 1rem 0;
    padding: .6rem .8rem;
    background: rgba(226,72,31,.12);
    border: 1px solid var(--safelight-dim);
    border-radius: 8px;
    color: var(--paper);
    font-size: .82rem;
  }
  .browse-error[hidden] { display: none; }

  .browse-list { flex: 1; overflow-y: auto; padding: .5rem; min-height: 12rem; }
  .browse-row {
    width: 100%;
    display: flex; align-items: center; gap: .65rem;
    background: transparent; border: none; border-radius: 8px;
    color: var(--paper); text-align: left;
    padding: .55rem .7rem;
    font-family: var(--serif);
    font-size: .92rem;
    cursor: pointer;
    position: relative;
  }
  .browse-row::before {
    content: "";
    position: absolute; left: 0; top: 4px; bottom: 4px; width: 3px;
    background: var(--safelight);
    transform: scaleY(0);
    transform-origin: center;
    transition: transform .15s ease;
  }
  .browse-row:hover { background: var(--bg-card); }
  .browse-row:hover::before { transform: scaleY(1); }
  .browse-row .chev { margin-left: auto; color: var(--gold); font-family: var(--mono); }
  .browse-row svg { flex: none; color: var(--paper-dim); }

  .browse-empty { padding: 2.5rem 1rem; text-align: center; color: var(--muted); font-style: italic; }

  .browse-footer {
    display: flex; align-items: center; gap: .75rem;
    padding: .85rem 1rem;
    border-top: 1px solid var(--rule);
    background: var(--bg-card);
  }
  .browse-jump {
    flex: 1; min-width: 0;
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 8px;
    color: var(--paper);
    font-family: var(--mono);
    font-size: .8rem;
    padding: .5rem .65rem;
  }
  .browse-footer-actions { display: flex; gap: .5rem; flex: none; }
  .browse-footer .btn-stamp, .browse-footer .btn-ghost { padding: .55rem 1.1rem; font-size: .75rem; }

  @media (max-width: 640px) {
    .shutter-btn { margin-left: 0; }
    .frame { width: 45vw; max-width: 210px; }
  }
</style>
</head>
<body>
<svg width="0" height="0" style="position:absolute" aria-hidden="true">
  <filter id="pencil-wobble" x="-30%" y="-30%" width="160%" height="160%">
    <feTurbulence type="fractalNoise" baseFrequency="0.9" numOctaves="2" seed="7" result="noise"/>
    <feDisplacementMap in="SourceGraphic" in2="noise" scale="6"/>
  </filter>
</svg>
<div class="grain" aria-hidden="true"></div>
<div class="filmstrip-edge left" aria-hidden="true"></div>
<div class="filmstrip-edge right" aria-hidden="true"></div>

<div class="page">
  <div class="leader"></div>
  <header class="site-header">
    <h1>photo&#8202;&middot;&#8202;dedupe</h1>
    <div class="subtitle">Contact Sheet Review</div>
  </header>

  {{if .BannerText}}<div class="banner{{if .BannerError}} error{{end}}">{{.BannerText}}</div>{{end}}

  <form id="scan-form" method="POST" action="/scan"></form>

  <section class="ticket" aria-label="Scan settings">
    <div class="field">
      <label for="directory-field">Roll &middot; Directory</label>
      <div class="field-control">
        <input type="text" id="directory-field" name="directory" form="scan-form" value="{{.Root}}" placeholder="Choose a folder to scan&hellip;" readonly{{if .Scanning}} disabled{{end}}>
        <button type="button" id="browse-open-btn" class="btn-browse" aria-haspopup="dialog" aria-controls="browse-modal"{{if .Scanning}} disabled{{end}}>
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 6a1 1 0 0 1 1-1h5l2 2h9a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6z"/></svg>
          <span>Browse&hellip;</span>
        </button>
      </div>
    </div>

    <div class="dials-row">
      <div class="dial">
        <label for="gap-text">Gap</label>
        <div class="dial-control">
          <input type="range" id="gap-range" min="5" max="900" step="5" aria-label="Gap in seconds">
          <input type="text" id="gap-text" name="gap" form="scan-form" value="{{.GapStr}}" size="6">
        </div>
      </div>
      <div class="dial">
        <label for="similarity-text">Match</label>
        <div class="dial-control">
          <input type="range" id="similarity-range" min="0" max="32" step="1" aria-label="Similarity threshold">
          <input type="text" id="similarity-text" name="similarity" form="scan-form" value="{{.SimStr}}" size="4">
        </div>
      </div>
      <div class="dial">
        <label for="blur-text">Blur</label>
        <div class="dial-control">
          <input type="range" id="blur-range" min="0" max="20000000" step="100000" aria-label="Blur threshold">
          <input type="text" id="blur-text" name="blur" form="scan-form" value="{{.BlurStr}}" size="8">
        </div>
      </div>
      <button type="submit" form="scan-form" class="shutter-btn" aria-label="Run scan"{{if .Scanning}} disabled{{end}}>SCAN</button>
    </div>

    <div id="develop-panel" class="develop-panel"{{if not .Scanning}} hidden{{end}}>
      <div class="develop-head">
        <span class="develop-dot" aria-hidden="true"></span>
        <span class="develop-label" id="develop-label">Reading your library&hellip;</span>
        <span class="develop-count" id="develop-count"></span>
      </div>
      <div class="develop-track">
        <div class="develop-fill indeterminate" id="develop-fill"></div>
      </div>
      <div class="develop-path" id="develop-path"></div>
    </div>
  </section>

  {{if .HasPlan}}
  <div class="actions">
    <form method="POST" action="/apply">
      <button type="submit" class="btn-stamp"{{if .Scanning}} disabled{{end}}>Apply &mdash; print keepers, archive rejects</button>
    </form>
    <form method="POST" action="/restore">
      <button type="submit" class="btn-ghost"{{if .Scanning}} disabled{{end}}>&#8630; Restore</button>
    </form>
  </div>

  {{if .Warnings}}
  <div class="warnings">{{len .Warnings}} spoiled frame(s) skipped &mdash; never a deletion candidate &mdash; see the terminal log for details.</div>
  {{end}}

  {{if not .Groups}}
  <p class="empty">No duplicate groups found.</p>
  {{end}}

  {{range $i, $g := .Groups}}
  <article class="group" style="--i: {{$i}}">
    <div class="group-head">
      <span class="roll-no">Group #{{$g.ID}}</span>
      <span class="frame-count">1 keeper &middot; {{len $g.Losers}} reject{{if ne (len $g.Losers) 1}}s{{end}}</span>
    </div>
    <div class="sprockets"></div>
    <div class="filmstrip">
      <div class="frame">
        <div class="frame-photo">
          <img src="{{$g.Winner.URL}}" alt="{{$g.Winner.Base}}" loading="lazy">
          <svg class="mark" viewBox="0 0 120 120" aria-hidden="true"><ellipse cx="60" cy="60" rx="52" ry="40" fill="none" stroke="#e2481f" stroke-width="5" filter="url(#pencil-wobble)"/></svg>
          <span class="stamp">Select</span>
        </div>
        <div class="frame-caption">{{$g.Winner.Base}}<br><span class="exif">{{$g.Winner.Width}}&times;{{$g.Winner.Height}} &middot; {{$g.Winner.SizeMB}} &middot; sharp {{printf "%.0f" $g.Winner.Sharpness}}</span></div>
      </div>
      {{range $g.Losers}}
      <div class="frame">
        <div class="frame-photo">
          <img src="{{.URL}}" alt="{{.Base}}" loading="lazy">
          <svg class="mark" viewBox="0 0 120 120" aria-hidden="true">
            <line x1="18" y1="18" x2="102" y2="102" stroke="#e2481f" stroke-width="6" filter="url(#pencil-wobble)"/>
            <line x1="102" y1="18" x2="18" y2="102" stroke="#e2481f" stroke-width="6" filter="url(#pencil-wobble)"/>
          </svg>
          <span class="stamp">Reject</span>
        </div>
        <div class="frame-caption">{{.Base}}<br><span class="exif">{{.Width}}&times;{{.Height}} &middot; {{.SizeMB}} &middot; sharp {{printf "%.0f" .Sharpness}}</span></div>
      </div>
      {{end}}
    </div>
    <div class="sprockets"></div>
  </article>
  {{end}}
  {{end}}
</div>

<div id="browse-backdrop" class="browse-backdrop" hidden></div>
<div id="browse-modal" class="browse-modal" role="dialog" aria-modal="true" aria-label="Choose a folder" hidden>
  <div class="browse-panel">
    <div class="browse-crumbs" id="browse-crumbs"></div>
    <div class="browse-shortcuts" id="browse-shortcuts"></div>
    <div class="browse-error" id="browse-error" hidden></div>
    <div class="browse-list" id="browse-list"></div>
    <div class="browse-footer">
      <input type="text" id="browse-path-input" class="browse-jump" placeholder="Jump to a path&hellip;" aria-label="Jump to a path">
      <div class="browse-footer-actions">
        <button type="button" id="browse-cancel-btn" class="btn-ghost">Cancel</button>
        <button type="button" id="browse-use-btn" class="btn-stamp">Use this folder</button>
      </div>
    </div>
  </div>
</div>

<script>
(function () {
  'use strict';

  var INITIAL_SCANNING = {{.Scanning}};

  var FOLDER_SVG = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6a1 1 0 0 1 1-1h5l2 2h9a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6z"/></svg>';

  var backdrop = document.getElementById('browse-backdrop');
  var modal = document.getElementById('browse-modal');
  var panel = modal.querySelector('.browse-panel');
  var crumbsEl = document.getElementById('browse-crumbs');
  var shortcutsEl = document.getElementById('browse-shortcuts');
  var listEl = document.getElementById('browse-list');
  var errorEl = document.getElementById('browse-error');
  var pathInput = document.getElementById('browse-path-input');
  var useBtn = document.getElementById('browse-use-btn');
  var dirField = document.getElementById('directory-field');

  var currentPath = '';

  function escapeHTML(s) {
    return s.replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function openBrowser() {
    backdrop.hidden = false;
    modal.hidden = false;
    document.body.classList.add('modal-open');
    panel.style.animation = 'none';
    panel.offsetHeight;
    panel.style.animation = '';
    load(dirField.value || '');
  }

  function closeBrowser() {
    backdrop.hidden = true;
    modal.hidden = true;
    document.body.classList.remove('modal-open');
  }

  function load(path) {
    listEl.setAttribute('aria-busy', 'true');
    fetch('/browse?path=' + encodeURIComponent(path))
      .then(function (res) { return res.json(); })
      .then(function (data) {
        currentPath = data.path;
        pathInput.value = data.path;
        renderCrumbs(data.path);
        renderShortcuts(data.shortcuts || []);
        if (data.error) {
          errorEl.textContent = "Can't open " + data.path + ': ' + data.error;
          errorEl.hidden = false;
          listEl.innerHTML = '';
        } else {
          errorEl.hidden = true;
          renderList(data.entries || []);
        }
      })
      .catch(function () {
        errorEl.textContent = 'Could not reach the local server.';
        errorEl.hidden = false;
      })
      .finally(function () {
        listEl.removeAttribute('aria-busy');
      });
  }

  function renderCrumbs(path) {
    crumbsEl.innerHTML = '';
    var parts = path.split('/').filter(Boolean);

    var root = document.createElement('button');
    root.type = 'button';
    root.className = 'crumb' + (parts.length === 0 ? ' current' : '');
    root.textContent = '/';
    root.addEventListener('click', function () { load('/'); });
    crumbsEl.appendChild(root);

    var acc = '';
    parts.forEach(function (part, i) {
      acc += '/' + part;
      var target = acc;
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'crumb' + (i === parts.length - 1 ? ' current' : '');
      b.textContent = part;
      b.addEventListener('click', function () { load(target); });
      crumbsEl.appendChild(b);
    });
  }

  function renderShortcuts(shortcuts) {
    shortcutsEl.innerHTML = '';
    shortcuts.forEach(function (sc) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'shortcut-pill';
      b.textContent = sc.name;
      b.addEventListener('click', function () { load(sc.path); });
      shortcutsEl.appendChild(b);
    });
  }

  function renderList(entries) {
    listEl.innerHTML = '';
    if (entries.length === 0) {
      var empty = document.createElement('div');
      empty.className = 'browse-empty';
      empty.textContent = 'No subfolders here.';
      listEl.appendChild(empty);
      return;
    }
    entries.forEach(function (entry) {
      var row = document.createElement('button');
      row.type = 'button';
      row.className = 'browse-row';
      row.innerHTML = FOLDER_SVG + '<span>' + escapeHTML(entry.name) + '</span><span class="chev">&rsaquo;</span>';
      row.addEventListener('click', function () { load(entry.path); });
      listEl.appendChild(row);
    });
  }

  function bindDial(rangeId, textId) {
    var range = document.getElementById(rangeId);
    var text = document.getElementById(textId);
    var initial = parseInt(text.value, 10);
    if (!isNaN(initial)) {
      var min = parseInt(range.min, 10), max = parseInt(range.max, 10);
      range.value = Math.min(Math.max(initial, min), max);
    }
    range.addEventListener('input', function () { text.value = range.value; });
  }
  function bindDurationDial(rangeId, textId) {
    var range = document.getElementById(rangeId);
    var text = document.getElementById(textId);
    var initial = parseInt(text.value, 10);
    if (!isNaN(initial)) {
      var min = parseInt(range.min, 10), max = parseInt(range.max, 10);
      range.value = Math.min(Math.max(initial, min), max);
    }
    range.addEventListener('input', function () { text.value = range.value + 's'; });
  }

  bindDurationDial('gap-range', 'gap-text');
  bindDial('similarity-range', 'similarity-text');
  bindDial('blur-range', 'blur-text');

  document.getElementById('browse-open-btn').addEventListener('click', openBrowser);
  dirField.addEventListener('click', openBrowser);
  document.getElementById('browse-cancel-btn').addEventListener('click', closeBrowser);
  backdrop.addEventListener('click', closeBrowser);
  useBtn.addEventListener('click', function () {
    dirField.value = currentPath;
    closeBrowser();
  });
  pathInput.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') {
      e.preventDefault();
      load(pathInput.value);
    }
  });
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && !modal.hidden) closeBrowser();
  });

  var developPanel = document.getElementById('develop-panel');
  var developLabel = document.getElementById('develop-label');
  var developCount = document.getElementById('develop-count');
  var developFill = document.getElementById('develop-fill');
  var developPath = document.getElementById('develop-path');

  function pollScanProgress() {
    fetch('/scan/progress')
      .then(function (res) { return res.json(); })
      .then(function (data) {
        if (!data.active) {
          window.location.reload();
          return;
        }
        developPanel.hidden = false;
        if (data.total > 0) {
          developFill.classList.remove('indeterminate');
          var pct = Math.min(100, Math.round((data.current / data.total) * 100));
          developFill.style.width = pct + '%';
          developLabel.textContent = 'Developing…';
          developCount.textContent = data.current + ' / ' + data.total;
        } else {
          developFill.classList.add('indeterminate');
          developLabel.textContent = 'Reading your library…';
          developCount.textContent = '';
        }
        developPath.textContent = data.path || '';
        setTimeout(pollScanProgress, 400);
      })
      .catch(function () { setTimeout(pollScanProgress, 800); });
  }

  if (INITIAL_SCANNING) {
    pollScanProgress();
  }
})();
</script>
</body>
</html>
`
