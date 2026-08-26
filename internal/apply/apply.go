// Package apply performs (and reverses) the filesystem side effects
// of a plan: moving winners into a "kept" folder and losers into a
// quarantine folder, both on the same volume, and restoring either
// back out again. Nothing is ever deleted — apply only relocates
// files, so the quarantine folder is yours to review and delete
// yourself once you trust the results.
package apply

import (
	"os"
	"path/filepath"

	"github.com/a7madM/photo-dedupe/internal/filehash"
	"github.com/a7madM/photo-dedupe/internal/plan"
)

// KeptDirName is the folder under a plan's Root that winners are
// moved into. Not dot-prefixed — it holds real results you'll want to
// browse in Finder. Scans always exclude it, so apply/restore runs
// stay idempotent and safe to repeat.
const KeptDirName = "dedupe-kept"

// QuarantineDirName is the folder under a plan's Root that losers are
// moved into. Not dot-prefixed, for the same reason as KeptDirName —
// you need to see it to review and delete it yourself. Scans always
// exclude it, for the same reason as KeptDirName.
const QuarantineDirName = "dedupe-quarantine"

// Outcome describes what happened to one file during Apply or Restore.
type Outcome string

const (
	OutcomeMoved          Outcome = "moved"
	OutcomeSkippedDrift   Outcome = "skipped_drift"
	OutcomeSkippedMissing Outcome = "skipped_missing"
)

// Role distinguishes which side of a group a Result belongs to.
type Role string

const (
	RoleWinner Role = "winner"
	RoleLoser  Role = "loser"
)

// Result is the outcome for a single file during Apply or Restore.
type Result struct {
	Path    string
	Outcome Outcome
	Role    Role
}

// Apply moves every winner in p into KeptDirName and every loser into
// QuarantineDirName, both under p.Root and both preserving each
// file's path relative to p.Root. Before moving a file, it re-hashes
// it and compares against the ContentHash recorded in the plan; a
// mismatch or a missing file is skipped (never acted on) and reported
// rather than causing an error.
func Apply(p plan.Plan) ([]Result, error) {
	keptRoot := filepath.Join(p.Root, KeptDirName)
	quarantineRoot := filepath.Join(p.Root, QuarantineDirName)

	var results []Result
	for _, group := range p.Groups {
		r, err := relocateOne(p.Root, keptRoot, RoleWinner, group.Winner)
		if err != nil {
			return results, err
		}
		results = append(results, r)

		for _, loser := range group.Losers {
			r, err := relocateOne(p.Root, quarantineRoot, RoleLoser, loser)
			if err != nil {
				return results, err
			}
			results = append(results, r)
		}
	}
	return results, nil
}

// Restore reverses a prior Apply: every winner and loser in p is
// moved back from KeptDirName/QuarantineDirName to its original
// recorded path. A file whose relocated copy is missing (e.g. Apply
// skipped it, or it was already restored) is skipped and reported
// rather than erroring.
func Restore(p plan.Plan) ([]Result, error) {
	keptRoot := filepath.Join(p.Root, KeptDirName)
	quarantineRoot := filepath.Join(p.Root, QuarantineDirName)

	var results []Result
	for _, group := range p.Groups {
		r, err := restoreOne(p.Root, keptRoot, RoleWinner, group.Winner)
		if err != nil {
			return results, err
		}
		results = append(results, r)

		for _, loser := range group.Losers {
			r, err := restoreOne(p.Root, quarantineRoot, RoleLoser, loser)
			if err != nil {
				return results, err
			}
			results = append(results, r)
		}
	}
	return results, nil
}

func relocateOne(root, destRoot string, role Role, rec plan.FileRecord) (Result, error) {
	if _, err := os.Stat(rec.Path); os.IsNotExist(err) {
		return Result{Path: rec.Path, Outcome: OutcomeSkippedMissing, Role: role}, nil
	}

	currentHash, err := filehash.SHA256(rec.Path)
	if err != nil {
		return Result{}, err
	}
	if currentHash != rec.ContentHash {
		return Result{Path: rec.Path, Outcome: OutcomeSkippedDrift, Role: role}, nil
	}

	rel, err := filepath.Rel(root, rec.Path)
	if err != nil {
		return Result{}, err
	}
	dest := filepath.Join(destRoot, rel)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.Rename(rec.Path, dest); err != nil {
		return Result{}, err
	}

	return Result{Path: rec.Path, Outcome: OutcomeMoved, Role: role}, nil
}

func restoreOne(root, destRoot string, role Role, rec plan.FileRecord) (Result, error) {
	rel, err := filepath.Rel(root, rec.Path)
	if err != nil {
		return Result{}, err
	}
	relocated := filepath.Join(destRoot, rel)

	if _, err := os.Stat(relocated); os.IsNotExist(err) {
		return Result{Path: rec.Path, Outcome: OutcomeSkippedMissing, Role: role}, nil
	}

	if err := os.MkdirAll(filepath.Dir(rec.Path), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.Rename(relocated, rec.Path); err != nil {
		return Result{}, err
	}

	return Result{Path: rec.Path, Outcome: OutcomeMoved, Role: role}, nil
}
