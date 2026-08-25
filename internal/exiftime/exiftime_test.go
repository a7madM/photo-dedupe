package exiftime

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolve_NoEXIFData_FallsBackToMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-an-image.jpg")
	if err := os.WriteFile(path, []byte("not a real jpeg"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	mtime := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("setting mtime: %v", err)
	}

	got, source, err := Resolve(path)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if source != SourceMtime {
		t.Fatalf("source = %q, want %q", source, SourceMtime)
	}
	if !got.Equal(mtime) {
		t.Fatalf("time = %v, want %v", got, mtime)
	}
}

func TestResolve_MissingFile_ReturnsError(t *testing.T) {
	_, _, err := Resolve(filepath.Join(t.TempDir(), "missing.jpg"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}
