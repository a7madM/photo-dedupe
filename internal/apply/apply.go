// Package apply performs (and reverses) the filesystem side effects
// of a plan: moving losers into a same-volume quarantine folder, and
// restoring them back out again.
package apply

import (
	"os"
	"path/filepath"

	"github.com/a7madM/photo-dedupe/internal/filehash"
	"github.com/a7madM/photo-dedupe/internal/plan"
)

// QuarantineDirName is the hardcoded quarantine folder name, created
// under a plan's Root. Scans always exclude it, so apply/restore
// runs stay idempotent and safe to repeat.
const QuarantineDirName = ".dedupe-quarantine"

// Outcome describes what happened to one file during Apply or Restore.
type Outcome string

const (
	OutcomeMoved          Outcome = "moved"
	OutcomeSkippedDrift   Outcome = "skipped_drift"
	OutcomeSkippedMissing Outcome = "skipped_missing"
)

// Result is the outcome for a single loser file.
type Result struct {
	Path    string
	Outcome Outcome
}

// Apply moves every loser in p into QuarantineDirName under p.Root,
// preserving each file's path relative to p.Root. Before moving a
// file, it re-hashes it and compares against the ContentHash recorded
// in the plan; a mismatch or a missing file is skipped (never acted
// on) and reported rather than causing an error.
func Apply(p plan.Plan) ([]Result, error) {
	quarantineRoot := filepath.Join(p.Root, QuarantineDirName)

	var results []Result
	for _, group := range p.Groups {
		for _, loser := range group.Losers {
			result, err := applyOne(p.Root, quarantineRoot, loser)
			if err != nil {
				return results, err
			}
			results = append(results, result)
		}
	}
	return results, nil
}

// Restore reverses a prior Apply: every loser in p is moved back from
// QuarantineDirName to its original recorded path. A loser whose
// quarantined copy is missing (e.g. Apply skipped it, or it was
// already restored) is skipped and reported rather than erroring.
func Restore(p plan.Plan) ([]Result, error) {
	quarantineRoot := filepath.Join(p.Root, QuarantineDirName)

	var results []Result
	for _, group := range p.Groups {
		for _, loser := range group.Losers {
			result, err := restoreOne(p.Root, quarantineRoot, loser)
			if err != nil {
				return results, err
			}
			results = append(results, result)
		}
	}
	return results, nil
}

func restoreOne(root, quarantineRoot string, loser plan.FileRecord) (Result, error) {
	rel, err := filepath.Rel(root, loser.Path)
	if err != nil {
		return Result{}, err
	}
	quarantined := filepath.Join(quarantineRoot, rel)

	if _, err := os.Stat(quarantined); os.IsNotExist(err) {
		return Result{Path: loser.Path, Outcome: OutcomeSkippedMissing}, nil
	}

	if err := os.MkdirAll(filepath.Dir(loser.Path), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.Rename(quarantined, loser.Path); err != nil {
		return Result{}, err
	}

	return Result{Path: loser.Path, Outcome: OutcomeMoved}, nil
}

func applyOne(root, quarantineRoot string, loser plan.FileRecord) (Result, error) {
	if _, err := os.Stat(loser.Path); os.IsNotExist(err) {
		return Result{Path: loser.Path, Outcome: OutcomeSkippedMissing}, nil
	}

	currentHash, err := filehash.SHA256(loser.Path)
	if err != nil {
		return Result{}, err
	}
	if currentHash != loser.ContentHash {
		return Result{Path: loser.Path, Outcome: OutcomeSkippedDrift}, nil
	}

	rel, err := filepath.Rel(root, loser.Path)
	if err != nil {
		return Result{}, err
	}
	dest := filepath.Join(quarantineRoot, rel)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.Rename(loser.Path, dest); err != nil {
		return Result{}, err
	}

	return Result{Path: loser.Path, Outcome: OutcomeMoved}, nil
}
