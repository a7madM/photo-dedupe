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

func findResult(t *testing.T, results []Result, path string) Result {
	t.Helper()
	for _, r := range results {
		if r.Path == path {
			return r
		}
	}
	t.Fatalf("no result for %s in %+v", path, results)
	return Result{}
}

func TestApply_MovesWinnerIntoKeptAndLoserIntoQuarantine_PreservingRelativePath(t *testing.T) {
	root := t.TempDir()
	winnerPath := filepath.Join(root, "sub", "a.jpg")
	loserPath := filepath.Join(root, "sub", "b.jpg")
	writeFile(t, winnerPath, "winner content")
	writeFile(t, loserPath, "loser content")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{
				ID:     1,
				Winner: plan.FileRecord{Path: winnerPath, ContentHash: hashOf(t, winnerPath)},
				Losers: []plan.FileRecord{{Path: loserPath, ContentHash: hashOf(t, loserPath)}},
			},
		},
	}

	results, err := Apply(p)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2 (one winner, one loser)", results)
	}

	wr := findResult(t, results, winnerPath)
	if wr.Outcome != OutcomeMoved || wr.Role != RoleWinner {
		t.Fatalf("winner result = %+v, want Moved/RoleWinner", wr)
	}
	lr := findResult(t, results, loserPath)
	if lr.Outcome != OutcomeMoved || lr.Role != RoleLoser {
		t.Fatalf("loser result = %+v, want Moved/RoleLoser", lr)
	}

	if _, err := os.Stat(winnerPath); !os.IsNotExist(err) {
		t.Fatalf("winner file still exists at original path: %v", err)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Fatalf("loser file still exists at original path: %v", err)
	}

	kept := filepath.Join(root, KeptDirName, "sub", "a.jpg")
	data, err := os.ReadFile(kept)
	if err != nil {
		t.Fatalf("kept file not found at %s: %v", kept, err)
	}
	if string(data) != "winner content" {
		t.Fatalf("kept content = %q, want %q", data, "winner content")
	}

	quarantined := filepath.Join(root, QuarantineDirName, "sub", "b.jpg")
	data, err = os.ReadFile(quarantined)
	if err != nil {
		t.Fatalf("quarantined file not found at %s: %v", quarantined, err)
	}
	if string(data) != "loser content" {
		t.Fatalf("quarantined content = %q, want %q", data, "loser content")
	}
}

func TestApply_WinnerDrifted_SkippedAndNotMoved(t *testing.T) {
	root := t.TempDir()
	winnerPath := filepath.Join(root, "a.jpg")
	writeFile(t, winnerPath, "original content")
	staleHash := hashOf(t, winnerPath)
	writeFile(t, winnerPath, "content changed since scan")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{ID: 1, Winner: plan.FileRecord{Path: winnerPath, ContentHash: staleHash}},
		},
	}

	results, err := Apply(p)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeSkippedDrift || results[0].Role != RoleWinner {
		t.Fatalf("results = %+v, want one SkippedDrift/RoleWinner result", results)
	}
	if _, err := os.Stat(winnerPath); err != nil {
		t.Fatalf("drifted winner should remain at original path: %v", err)
	}
}

func TestApply_WinnerMissing_SkippedWithoutError(t *testing.T) {
	root := t.TempDir()
	missingPath := filepath.Join(root, "gone.jpg")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{ID: 1, Winner: plan.FileRecord{Path: missingPath, ContentHash: "whatever"}},
		},
	}

	results, err := Apply(p)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeSkippedMissing || results[0].Role != RoleWinner {
		t.Fatalf("results = %+v, want one SkippedMissing/RoleWinner result", results)
	}
}

func TestApply_DriftedLoserContent_SkippedAndNotMoved(t *testing.T) {
	root := t.TempDir()
	winnerPath := filepath.Join(root, "a.jpg")
	loserPath := filepath.Join(root, "b.jpg")
	writeFile(t, winnerPath, "winner content")
	writeFile(t, loserPath, "original content")
	staleHash := hashOf(t, loserPath)
	writeFile(t, loserPath, "content changed since scan")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{
				ID:     1,
				Winner: plan.FileRecord{Path: winnerPath, ContentHash: hashOf(t, winnerPath)},
				Losers: []plan.FileRecord{{Path: loserPath, ContentHash: staleHash}},
			},
		},
	}

	results, err := Apply(p)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2", results)
	}
	lr := findResult(t, results, loserPath)
	if lr.Outcome != OutcomeSkippedDrift || lr.Role != RoleLoser {
		t.Fatalf("loser result = %+v, want SkippedDrift/RoleLoser", lr)
	}
	if _, err := os.Stat(loserPath); err != nil {
		t.Fatalf("drifted loser should remain at original path: %v", err)
	}
}

func TestApply_MissingLoserFile_SkippedWithoutError(t *testing.T) {
	root := t.TempDir()
	winnerPath := filepath.Join(root, "a.jpg")
	writeFile(t, winnerPath, "winner content")
	missingPath := filepath.Join(root, "gone.jpg")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{
				ID:     1,
				Winner: plan.FileRecord{Path: winnerPath, ContentHash: hashOf(t, winnerPath)},
				Losers: []plan.FileRecord{{Path: missingPath, ContentHash: "whatever"}},
			},
		},
	}

	results, err := Apply(p)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2", results)
	}
	lr := findResult(t, results, missingPath)
	if lr.Outcome != OutcomeSkippedMissing || lr.Role != RoleLoser {
		t.Fatalf("loser result = %+v, want SkippedMissing/RoleLoser", lr)
	}
}

func TestRestore_ReversesApply_WinnerAndLoserBackAtOriginalPaths(t *testing.T) {
	root := t.TempDir()
	winnerPath := filepath.Join(root, "sub", "a.jpg")
	loserPath := filepath.Join(root, "sub", "b.jpg")
	writeFile(t, winnerPath, "winner content")
	writeFile(t, loserPath, "loser content")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{
				ID:     1,
				Winner: plan.FileRecord{Path: winnerPath, ContentHash: hashOf(t, winnerPath)},
				Losers: []plan.FileRecord{{Path: loserPath, ContentHash: hashOf(t, loserPath)}},
			},
		},
	}

	if _, err := Apply(p); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	results, err := Restore(p)
	if err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2", results)
	}
	wr := findResult(t, results, winnerPath)
	if wr.Outcome != OutcomeMoved || wr.Role != RoleWinner {
		t.Fatalf("winner result = %+v, want Moved/RoleWinner", wr)
	}
	lr := findResult(t, results, loserPath)
	if lr.Outcome != OutcomeMoved || lr.Role != RoleLoser {
		t.Fatalf("loser result = %+v, want Moved/RoleLoser", lr)
	}

	data, err := os.ReadFile(winnerPath)
	if err != nil || string(data) != "winner content" {
		t.Fatalf("restored winner content = %q, err %v, want %q", data, err, "winner content")
	}
	data, err = os.ReadFile(loserPath)
	if err != nil || string(data) != "loser content" {
		t.Fatalf("restored loser content = %q, err %v, want %q", data, err, "loser content")
	}

	if _, err := os.Stat(filepath.Join(root, KeptDirName, "sub", "a.jpg")); !os.IsNotExist(err) {
		t.Fatalf("kept copy should no longer exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, QuarantineDirName, "sub", "b.jpg")); !os.IsNotExist(err) {
		t.Fatalf("quarantined copy should no longer exist: %v", err)
	}
}

func TestRestore_RelocatedFileMissing_SkippedWithoutError(t *testing.T) {
	root := t.TempDir()
	winnerPath := filepath.Join(root, "a.jpg")
	loserPath := filepath.Join(root, "b.jpg")

	p := plan.Plan{
		Root: root,
		Groups: []plan.Group{
			{
				ID:     1,
				Winner: plan.FileRecord{Path: winnerPath, ContentHash: "whatever"},
				Losers: []plan.FileRecord{{Path: loserPath, ContentHash: "whatever"}},
			},
		},
	}

	results, err := Restore(p)
	if err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2", results)
	}
	wr := findResult(t, results, winnerPath)
	if wr.Outcome != OutcomeSkippedMissing || wr.Role != RoleWinner {
		t.Fatalf("winner result = %+v, want SkippedMissing/RoleWinner", wr)
	}
	lr := findResult(t, results, loserPath)
	if lr.Outcome != OutcomeSkippedMissing || lr.Role != RoleLoser {
		t.Fatalf("loser result = %+v, want SkippedMissing/RoleLoser", lr)
	}
}
