// Package scan orchestrates a full dedupe scan: walk a directory,
// resolve each image's capture time, cluster by time gap, filter
// each time-cluster into similarity groups by perceptual hash, pick a
// winner per similarity group, and assemble the result into a Plan.
package scan

import (
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/a7madM/photo-dedupe/internal/apply"
	"github.com/a7madM/photo-dedupe/internal/cluster"
	"github.com/a7madM/photo-dedupe/internal/exiftime"
	"github.com/a7madM/photo-dedupe/internal/filehash"
	"github.com/a7madM/photo-dedupe/internal/imagemetrics"
	"github.com/a7madM/photo-dedupe/internal/pick"
	"github.com/a7madM/photo-dedupe/internal/plan"
	"github.com/a7madM/photo-dedupe/internal/simgroup"
)

// Options controls a scan run. See the project spec for why each
// default was chosen; all are expected to need tuning against real
// photo libraries.
type Options struct {
	Root string

	// GapThreshold: consecutive shots within this gap belong to the
	// same time-cluster (candidate pool for similarity comparison).
	GapThreshold time.Duration

	// SimilarityThreshold: max perceptual-hash Hamming distance for
	// two images to be considered the same shot.
	SimilarityThreshold int

	// BlurThreshold: a candidate is excluded from winning if its
	// sharpness score is more than this far below the group's best.
	BlurThreshold float64

	// Progress, if non-nil, is called once per discovered file right
	// after that file's timestamp/metrics have been resolved (whether
	// or not that resolution succeeded). index is 1-based; total is
	// the number of files discover found. Files are resolved
	// concurrently (see Concurrency), so calls arrive in completion
	// order rather than discovery order — index still counts up from
	// 1 to total, one call per file, just not against a fixed path.
	// Optional — nil is a no-op.
	Progress func(index, total int, path string)

	// Concurrency caps how many files are decoded and hashed at once
	// — the expensive part of a scan (EXIF read, image decode,
	// perceptual hash, sharpness), and embarrassingly parallel since
	// each file is independent. Zero or negative defaults to
	// runtime.NumCPU().
	Concurrency int
}

// Warning records a file that was skipped rather than causing the
// whole scan to fail — corrupt/unreadable files are never a deletion
// candidate.
type Warning struct {
	Path   string
	Reason string
}

var supportedExt = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".heic": true,
	".heif": true,
}

type entry struct {
	path      string
	timestamp time.Time
	metrics   imagemetrics.Metrics
}

// Run performs a full scan and returns the resulting Plan along with
// any files that were skipped.
func Run(opts Options) (plan.Plan, []Warning, error) {
	paths, err := discover(opts.Root)
	if err != nil {
		return plan.Plan{}, nil, err
	}

	progress := opts.Progress
	if progress == nil {
		progress = func(int, int, string) {}
	}

	entries, warnings := resolveEntries(paths, opts.Concurrency, progress)

	byPath := make(map[string]entry, len(entries))
	items := make([]cluster.Item, 0, len(entries))
	for _, e := range entries {
		byPath[e.path] = e
		items = append(items, cluster.Item{Path: e.path, Timestamp: e.timestamp})
	}

	timeGroups := cluster.Group(items, opts.GapThreshold)

	var groups []plan.Group
	nextID := 1
	for _, timeGroup := range timeGroups {
		if len(timeGroup) < 2 {
			continue
		}
		groupEntries := make([]entry, len(timeGroup))
		for i, item := range timeGroup {
			groupEntries[i] = byPath[item.Path]
		}

		distFn := func(i, j int) int {
			d, err := groupEntries[i].metrics.Hash.Distance(groupEntries[j].metrics.Hash)
			if err != nil {
				return opts.SimilarityThreshold + 1 // treat as dissimilar
			}
			return d
		}
		simGroups := simgroup.Group(len(groupEntries), opts.SimilarityThreshold, distFn)

		for _, sg := range simGroups {
			if len(sg) < 2 {
				continue
			}
			candidates := make([]pick.Candidate, len(sg))
			for i, idx := range sg {
				e := groupEntries[idx]
				candidates[i] = pick.Candidate{
					Path:      e.path,
					Sharpness: e.metrics.Sharpness,
					Width:     e.metrics.Width,
					Height:    e.metrics.Height,
					SizeBytes: e.metrics.SizeBytes,
				}
			}
			winner, losers := pick.Pick(candidates, opts.BlurThreshold)

			winnerRecord, err := toFileRecord(winner)
			if err != nil {
				warnings = append(warnings, Warning{Path: winner.Path, Reason: "cannot hash file: " + err.Error()})
				continue
			}
			loserRecords := make([]plan.FileRecord, 0, len(losers))
			for _, l := range losers {
				r, err := toFileRecord(l)
				if err != nil {
					warnings = append(warnings, Warning{Path: l.Path, Reason: "cannot hash file: " + err.Error()})
					continue
				}
				loserRecords = append(loserRecords, r)
			}
			if len(loserRecords) == 0 {
				continue
			}

			groups = append(groups, plan.Group{ID: nextID, Winner: winnerRecord, Losers: loserRecords})
			nextID++
		}
	}

	p := plan.Plan{
		Version:     1,
		Root:        opts.Root,
		GapSeconds:  int(opts.GapThreshold.Seconds()),
		GeneratedAt: time.Now().UTC(),
		Groups:      groups,
	}
	return p, warnings, nil
}

// resolveEntries resolves every path's timestamp and image metrics
// concurrently across a bounded worker pool — the slow part of a scan
// (EXIF parsing, image decode, perceptual hash, sharpness), and
// independent per file, so this is where multiple cores actually pay
// off. progress is serialized behind the same lock that collects
// results, so callers see one call per file with a strictly
// increasing count, same as a sequential scan — just not necessarily
// in discovery order.
func resolveEntries(paths []string, concurrency int, progress func(index, total int, path string)) ([]entry, []Warning) {
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}
	if concurrency > len(paths) {
		concurrency = len(paths)
	}

	var (
		mu       sync.Mutex
		entries  []entry
		warnings []Warning
		done     int
	)

	jobs := make(chan string)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				e, warn := resolveOne(path)

				mu.Lock()
				if warn != nil {
					warnings = append(warnings, *warn)
				} else {
					entries = append(entries, e)
				}
				done++
				progress(done, len(paths), path)
				mu.Unlock()
			}
		}()
	}
	for _, path := range paths {
		jobs <- path
	}
	close(jobs)
	wg.Wait()

	return entries, warnings
}

// resolveOne resolves a single file's timestamp and image metrics.
// Exactly one of the return values is populated.
func resolveOne(path string) (entry, *Warning) {
	ts, _, err := exiftime.Resolve(path)
	if err != nil {
		return entry{}, &Warning{Path: path, Reason: "cannot resolve timestamp: " + err.Error()}
	}
	m, err := imagemetrics.Compute(path)
	if err != nil {
		return entry{}, &Warning{Path: path, Reason: "cannot decode image: " + err.Error()}
	}
	return entry{path: path, timestamp: ts, metrics: m}, nil
}

func toFileRecord(c pick.Candidate) (plan.FileRecord, error) {
	hash, err := filehash.SHA256(c.Path)
	if err != nil {
		return plan.FileRecord{}, err
	}
	return plan.FileRecord{
		Path:        c.Path,
		ContentHash: hash,
		Width:       c.Width,
		Height:      c.Height,
		Sharpness:   c.Sharpness,
		SizeBytes:   c.SizeBytes,
	}, nil
}

// discover walks root recursively and returns every supported image
// file, excluding the kept and quarantine directories so re-scanning
// the same root after an apply is safe.
func discover(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == apply.KeptDirName || d.Name() == apply.QuarantineDirName {
				return filepath.SkipDir
			}
			return nil
		}
		if supportedExt[strings.ToLower(filepath.Ext(path))] {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}
