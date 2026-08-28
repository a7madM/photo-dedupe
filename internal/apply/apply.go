// Package apply performs (and reverses) the filesystem side effects
// of a plan: moving winners into a "kept" folder and losers into a
// quarantine folder, both on the same volume, and restoring either
// back out again. Nothing is ever deleted — apply only relocates
// files, so the quarantine folder is yours to review and delete
// yourself once you trust the results.
package apply

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

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

// relocateJob is one file due to move into destRoot, flattened out of
// a plan's groups so its (expensive, read-only) drift check can run
// concurrently ahead of the (cheap, mutating) move.
type relocateJob struct {
	role     Role
	rec      plan.FileRecord
	destRoot string
}

func flattenApplyJobs(p plan.Plan, keptRoot, quarantineRoot string) []relocateJob {
	jobs := make([]relocateJob, 0, len(p.Groups)*2)
	for _, g := range p.Groups {
		jobs = append(jobs, relocateJob{RoleWinner, g.Winner, keptRoot})
		for _, l := range g.Losers {
			jobs = append(jobs, relocateJob{RoleLoser, l, quarantineRoot})
		}
	}
	return jobs
}

// Apply moves every winner in p into KeptDirName and every loser into
// QuarantineDirName, both under p.Root and both preserving each
// file's path relative to p.Root. Before moving a file, it re-hashes
// it and compares against the ContentHash recorded in the plan; a
// mismatch or a missing file is skipped (never acted on) and reported
// rather than causing an error.
//
// Every file's hash check runs concurrently first — full-file SHA-256
// over what can be many large photos is the expensive part, and each
// check is independent and read-only, so it fans out the same way
// scan's resolveEntries does. The actual moves then run sequentially,
// in the plan's original group order, exactly as a fully sequential
// Apply would: a real check failure (as opposed to an ordinary
// missing/drifted skip) is surfaced before any file is moved, and a
// real move failure still stops the run at that point, partial
// results included.
func Apply(p plan.Plan) ([]Result, error) {
	keptRoot := filepath.Join(p.Root, KeptDirName)
	quarantineRoot := filepath.Join(p.Root, QuarantineDirName)

	jobs := flattenApplyJobs(p, keptRoot, quarantineRoot)
	checks, err := checkAll(jobs)
	if err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(jobs))
	for i, j := range jobs {
		if c := checks[i]; !c.proceed {
			results = append(results, Result{Path: j.rec.Path, Outcome: c.outcome, Role: j.role})
			continue
		}
		r, err := moveOne(p.Root, j.destRoot, j.role, j.rec)
		if err != nil {
			return results, err
		}
		results = append(results, r)
	}
	return results, nil
}

// driftCheck is one job's verdict: proceed is false when the file is
// missing or its content has drifted since the scan (an ordinary,
// reported skip); true means it's safe to move.
type driftCheck struct {
	outcome Outcome
	proceed bool
}

// checkAll runs checkRelocate for every job across a bounded worker
// pool. Each goroutine only ever writes to its own job's index, so
// results/errs need no locking despite being shared across workers.
func checkAll(jobs []relocateJob) ([]driftCheck, error) {
	results := make([]driftCheck, len(jobs))
	errs := make([]error, len(jobs))

	concurrency := runtime.NumCPU()
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}

	idxs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idxs {
				outcome, proceed, err := checkRelocate(jobs[i].rec)
				results[i] = driftCheck{outcome, proceed}
				errs[i] = err
			}
		}()
	}
	for i := range jobs {
		idxs <- i
	}
	close(idxs)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("checking %s: %w", jobs[i].rec.Path, err)
		}
	}
	return results, nil
}

// checkRelocate reports whether rec is safe to move. A non-nil error
// means its hash couldn't even be computed (e.g. a permission or I/O
// failure on a file that does exist) — a real, unexpected failure,
// distinct from the ordinary missing/drifted skips.
func checkRelocate(rec plan.FileRecord) (outcome Outcome, proceed bool, err error) {
	if _, err := os.Stat(rec.Path); os.IsNotExist(err) {
		return OutcomeSkippedMissing, false, nil
	}

	currentHash, err := filehash.SHA256(rec.Path)
	if err != nil {
		return "", false, err
	}
	if currentHash != rec.ContentHash {
		return OutcomeSkippedDrift, false, nil
	}
	return "", true, nil
}

// moveOne performs the actual filesystem move for a file already
// verified safe to relocate.
func moveOne(root, destRoot string, role Role, rec plan.FileRecord) (Result, error) {
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
