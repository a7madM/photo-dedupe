package webui

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a7madM/photo-dedupe/internal/apply"
)

func writePNG(t *testing.T, path string, c color.RGBA, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatalf("encode: %v", err)
	}
	f.Close()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// writeDuplicatePair writes two identical, close-in-time PNGs into
// root, guaranteed to land in the same group under scan's defaults.
func writeDuplicatePair(t *testing.T, root string) {
	t.Helper()
	base := time.Now()
	writePNG(t, filepath.Join(root, "a.png"), color.RGBA{200, 50, 50, 255}, base)
	writePNG(t, filepath.Join(root, "b.png"), color.RGBA{200, 50, 50, 255}, base.Add(2*time.Second))
}

// doScan submits a scan and waits for the background goroutine it
// kicks off to finish, so callers see the same synchronous behavior
// the handler had before scanning moved off the request goroutine.
func doScan(t *testing.T, s *Server, dir string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"directory": {dir}}
	req := httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	waitForScanToFinish(t, s)
	return rr
}

func waitForScanToFinish(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		scanning := s.scanning
		s.mu.Unlock()
		if !scanning {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("scan did not finish within 5s")
}

func TestIndex_NoScanYet_ShowsScanForm(t *testing.T) {
	s := New()

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `name="directory"`) {
		t.Fatalf("expected a directory input on first load, got: %s", body)
	}
}

func TestScan_PopulatesGalleryFromDirectory(t *testing.T) {
	root := t.TempDir()
	writeDuplicatePair(t, root)

	s := New()
	rr := doScan(t, s, root)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	s.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr2.Body.String()
	if !strings.Contains(body, "a.png") || !strings.Contains(body, "b.png") {
		t.Fatalf("expected group filenames in gallery, got: %s", body)
	}
}

func TestScan_RejectsGET(t *testing.T) {
	s := New()

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/scan", nil))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestScan_MissingDirectory_ReportsErrorWithoutCrashing(t *testing.T) {
	s := New()

	rr := doScan(t, s, "")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect back with an error banner)", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	s.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rr2.Body.String(), "directory") {
		t.Fatalf("expected an error mentioning the missing directory, got: %s", rr2.Body.String())
	}
}

func TestScan_NonexistentDirectory_ReportsErrorWithoutCrashing(t *testing.T) {
	s := New()

	rr := doScan(t, s, filepath.Join(t.TempDir(), "does-not-exist"))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	s.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("index after a failed scan should still render: status = %d", rr2.Code)
	}
}

func TestImage_ServesAllowedPathAfterScan(t *testing.T) {
	root := t.TempDir()
	writeDuplicatePair(t, root)

	s := New()
	doScan(t, s, root)

	req := httptest.NewRequest(http.MethodGet, "/image?path="+url.QueryEscape(filepath.Join(root, "a.png")), nil)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
}

func TestImage_RejectsPathNotInPlan(t *testing.T) {
	root := t.TempDir()
	writePNG(t, filepath.Join(root, "other.png"), color.RGBA{1, 2, 3, 255}, time.Now())

	s := New() // no scan performed at all yet

	req := httptest.NewRequest(http.MethodGet, "/image?path="+url.QueryEscape(filepath.Join(root, "other.png")), nil)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestApply_WithoutScan_ReturnsBadRequest(t *testing.T) {
	s := New()

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/apply", nil))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestApply_MovesFilesAndRecordsSummary(t *testing.T) {
	root := t.TempDir()
	writeDuplicatePair(t, root)

	s := New()
	doScan(t, s, root)

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rr.Code)
	}

	if _, err := os.Stat(filepath.Join(root, apply.KeptDirName)); err != nil {
		t.Fatalf("expected %s: %v", apply.KeptDirName, err)
	}

	rr2 := httptest.NewRecorder()
	s.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr2.Body.String()
	if !strings.Contains(body, "apply complete") || !strings.Contains(body, "1 winners moved") || !strings.Contains(body, "1 losers moved") {
		t.Fatalf("expected apply summary in index body, got: %s", body)
	}
}

func TestApply_RejectsGET(t *testing.T) {
	root := t.TempDir()
	writeDuplicatePair(t, root)

	s := New()
	doScan(t, s, root)

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/apply", nil))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func doScanProgress(t *testing.T, s *Server) scanProgressView {
	t.Helper()
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/scan/progress", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp scanProgressView
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v, body: %s", err, rr.Body.String())
	}
	return resp
}

func TestScanProgress_ReflectsActiveScanState(t *testing.T) {
	s := New()

	s.mu.Lock()
	s.scanning = true
	s.scanDirectory = "/photos"
	s.scanCurrent = 3
	s.scanTotal = 10
	s.scanPath = "/photos/IMG_003.jpg"
	s.mu.Unlock()

	resp := doScanProgress(t, s)
	if !resp.Active || resp.Current != 3 || resp.Total != 10 || resp.Path != "IMG_003.jpg" {
		t.Fatalf("progress = %+v, want active with current=3 total=10 path=IMG_003.jpg", resp)
	}

	s.mu.Lock()
	s.scanning = false
	s.mu.Unlock()

	resp = doScanProgress(t, s)
	if resp.Active {
		t.Fatalf("progress = %+v, want active=false once scan finishes", resp)
	}
}

func TestScan_RejectsWhileAnotherScanIsRunning(t *testing.T) {
	root := t.TempDir()
	s := New()

	s.mu.Lock()
	s.scanning = true
	s.mu.Unlock()

	form := url.Values{"directory": {root}}
	req := httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	s.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rr2.Body.String(), "already running") {
		t.Fatalf("expected an already-running banner, got: %s", rr2.Body.String())
	}
}

func TestApply_WhileScanning_ReturnsConflict(t *testing.T) {
	root := t.TempDir()
	writeDuplicatePair(t, root)

	s := New()
	doScan(t, s, root) // establishes a real plan first

	s.mu.Lock()
	s.scanning = true
	s.mu.Unlock()

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}

func doBrowse(t *testing.T, s *Server, path string) browseResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/browse?path="+url.QueryEscape(path), nil)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp browseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v, body: %s", err, rr.Body.String())
	}
	return resp
}

func TestBrowse_ListsSubdirectoriesOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "b-folder"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "a-folder"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "not-a-dir.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	s := New()
	resp := doBrowse(t, s, root)

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("entries = %v, want exactly the 2 non-hidden subdirectories", resp.Entries)
	}
	if resp.Entries[0].Name != "a-folder" || resp.Entries[1].Name != "b-folder" {
		t.Fatalf("entries = %v, want sorted [a-folder, b-folder]", resp.Entries)
	}
	if resp.Parent != filepath.Dir(root) {
		t.Fatalf("parent = %q, want %q", resp.Parent, filepath.Dir(root))
	}
}

func TestBrowse_EmptyPathDefaultsToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory available in this environment")
	}

	s := New()
	resp := doBrowse(t, s, "")

	if resp.Path != filepath.Clean(home) {
		t.Fatalf("path = %q, want home directory %q", resp.Path, home)
	}
}

func TestBrowse_NonexistentPath_ReportsErrorWithoutCrashing(t *testing.T) {
	s := New()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	resp := doBrowse(t, s, missing)

	if resp.Error == "" {
		t.Fatalf("expected an error for a nonexistent path, got: %+v", resp)
	}
}

func TestBrowse_RejectsPOST(t *testing.T) {
	s := New()

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/browse", nil))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestRestore_ReversesApply(t *testing.T) {
	root := t.TempDir()
	writeDuplicatePair(t, root)

	s := New()
	doScan(t, s, root)
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/apply", nil))

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/restore", nil))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rr.Code)
	}

	if _, err := os.Stat(filepath.Join(root, "a.png")); err != nil {
		t.Fatalf("expected a.png restored: %v", err)
	}
}
