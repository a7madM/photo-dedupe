package apply

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/a7madM/photo-dedupe/internal/filehash"
	"github.com/a7madM/photo-dedupe/internal/plan"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}
}

func hashOf(t *testing.T, path string) string {
	t.Helper()
	h, err := filehash.SHA256(path)
	if err != nil {
		t.Fatalf("hashing fixture file: %v", err)
	}
	return h
}

func TestApply_MovesLoserIntoQuarantine_PreservingRelativePath(t *testing.T) {
	root := t.TempDir()
	loserPath := filepath.Join(root, "sub", "b.jpg")
	writeFile(t, loserPath, "loser content")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{
				ID:     1,
				Winner: plan.FileRecord{Path: filepath.Join(root, "sub", "a.jpg")},
				Losers: []plan.FileRecord{
					{Path: loserPath, ContentHash: hashOf(t, loserPath)},
				},
			},
		},
	}
	writeFile(t, p.Groups[0].Winner.Path, "winner content")

	results, err := Apply(p)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if len(results) != 1 || results[0].Path != loserPath || results[0].Outcome != OutcomeMoved {
		t.Fatalf("results = %+v, want one Moved result for %s", results, loserPath)
	}

	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Fatalf("loser file still exists at original path: %v", err)
	}

	quarantined := filepath.Join(root, QuarantineDirName, "sub", "b.jpg")
	data, err := os.ReadFile(quarantined)
	if err != nil {
		t.Fatalf("quarantined file not found at %s: %v", quarantined, err)
	}
	if string(data) != "loser content" {
		t.Fatalf("quarantined content = %q, want %q", data, "loser content")
	}

	if _, err := os.Stat(p.Groups[0].Winner.Path); err != nil {
		t.Fatalf("winner file should be untouched: %v", err)
	}
}

func TestApply_DriftedContent_SkippedAndNotMoved(t *testing.T) {
	root := t.TempDir()
	loserPath := filepath.Join(root, "b.jpg")
	writeFile(t, loserPath, "original content")
	staleHash := hashOf(t, loserPath)

	writeFile(t, loserPath, "content changed since scan")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{ID: 1, Losers: []plan.FileRecord{{Path: loserPath, ContentHash: staleHash}}},
		},
	}

	results, err := Apply(p)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if len(results) != 1 || results[0].Outcome != OutcomeSkippedDrift {
		t.Fatalf("results = %+v, want one SkippedDrift result", results)
	}
	if _, err := os.Stat(loserPath); err != nil {
		t.Fatalf("drifted file should remain at original path: %v", err)
	}
}

func TestApply_MissingFile_SkippedWithoutError(t *testing.T) {
	root := t.TempDir()
	missingPath := filepath.Join(root, "gone.jpg")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{ID: 1, Losers: []plan.FileRecord{{Path: missingPath, ContentHash: "whatever"}}},
		},
	}

	results, err := Apply(p)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if len(results) != 1 || results[0].Outcome != OutcomeSkippedMissing {
		t.Fatalf("results = %+v, want one SkippedMissing result", results)
	}
}

func TestRestore_ReversesApply_FileBackAtOriginalPath(t *testing.T) {
	root := t.TempDir()
	loserPath := filepath.Join(root, "sub", "b.jpg")
	writeFile(t, loserPath, "loser content")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{ID: 1, Losers: []plan.FileRecord{{Path: loserPath, ContentHash: hashOf(t, loserPath)}}},
		},
	}

	if _, err := Apply(p); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	results, err := Restore(p)
	if err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if len(results) != 1 || results[0].Path != loserPath || results[0].Outcome != OutcomeMoved {
		t.Fatalf("results = %+v, want one Moved result for %s", results, loserPath)
	}

	data, err := os.ReadFile(loserPath)
	if err != nil {
		t.Fatalf("restored file not found at original path: %v", err)
	}
	if string(data) != "loser content" {
		t.Fatalf("restored content = %q, want %q", data, "loser content")
	}
	quarantined := filepath.Join(root, QuarantineDirName, "sub", "b.jpg")
	if _, err := os.Stat(quarantined); !os.IsNotExist(err) {
		t.Fatalf("quarantined copy should no longer exist: %v", err)
	}
}

func TestRestore_QuarantinedFileMissing_SkippedWithoutError(t *testing.T) {
	root := t.TempDir()
	loserPath := filepath.Join(root, "b.jpg")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{ID: 1, Losers: []plan.FileRecord{{Path: loserPath, ContentHash: "whatever"}}},
		},
	}

	results, err := Restore(p)
	if err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeSkippedMissing {
		t.Fatalf("results = %+v, want one SkippedMissing result", results)
	}
}
