// Package scan orchestrates a full dedupe scan: walk a directory,
// resolve each image's capture time, cluster by time gap, filter
// each time-cluster into similarity groups by perceptual hash, pick a
// winner per similarity group, and assemble the result into a Plan.
package scan

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

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

	// Limit caps how many of the directory's supported images a scan
	// processes, taken in os.ReadDir's filename order (so the same
	// subset is chosen on every run against an unchanged directory).
	// Files beyond the cap are simply left out — reported via
	// Plan.Stats, not as a Warning, since a Warning specifically means
	// a file was attempted and failed. Zero or negative means
	// unlimited; callers wanting the "large library" default of 1000
	// (this package doesn't impose one itself) should set it
	// explicitly.
	Limit int
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
	start := time.Now()

	paths, totalFound, err := discover(opts.Root, opts.Limit)

	if err != nil {
		return plan.Plan{}, nil, err
	}

	progress := opts.Progress
	if progress == nil {
		progress = func(int, int, string) {}
	}

	entries, warnings := resolveEntries(paths, opts.Concurrency, progress)

	var totalSizeBytes int64
	for _, e := range entries {
		totalSizeBytes += e.metrics.SizeBytes
	}

	groups, groupWarnings := buildGroups(entries, opts.GapThreshold, opts.SimilarityThreshold, opts.BlurThreshold)
	warnings = append(warnings, groupWarnings...)

	p := plan.Plan{
		Version:     1,
		Root:        opts.Root,
		GapSeconds:  int(opts.GapThreshold.Seconds()),
		GeneratedAt: time.Now().UTC(),
		Groups:      groups,
		Stats: plan.Stats{
			TotalFound:     totalFound,
			TotalImages:    len(paths),
			TotalSizeBytes: totalSizeBytes,
			Warnings:       len(warnings),
			DurationMS:     time.Since(start).Milliseconds(),
		},
	}
	return p, warnings, nil
}

// buildGroups time-clusters resolved entries, splits each time-cluster
// into similarity groups by perceptual hash, and turns each similarity
// group of two or more images into a plan.Group with a chosen winner.
func buildGroups(entries []entry, gap time.Duration, simThreshold int, blurThreshold float64) ([]plan.Group, []Warning) {
	timestamps := make([]time.Time, len(entries))
	for i, e := range entries {
		timestamps[i] = e.timestamp
	}

	var groups []plan.Group
	var warnings []Warning
	nextID := 1
	for _, timeIdxs := range cluster.Group(timestamps, gap) {
		if len(timeIdxs) < 2 {
			continue
		}
		timeClusterEntries := selectEntries(entries, timeIdxs)

		for _, simIdxs := range similarityGroups(timeClusterEntries, simThreshold) {
			if len(simIdxs) < 2 {
				continue
			}

			g, ok, buildWarnings := buildGroup(nextID, selectEntries(timeClusterEntries, simIdxs), blurThreshold)
			warnings = append(warnings, buildWarnings...)
			if !ok {
				continue
			}
			groups = append(groups, g)
			nextID++
		}
	}
	return groups, warnings
}

// selectEntries returns the entries at idxs, in idxs' order.
func selectEntries(entries []entry, idxs []int) []entry {
	out := make([]entry, len(idxs))
	for i, idx := range idxs {
		out[i] = entries[idx]
	}
	return out
}

// similarityGroups partitions entries already known to be close in
// time into similarity clusters by perceptual-hash Hamming distance.
func similarityGroups(entries []entry, threshold int) [][]int {
	distFn := func(i, j int) int {
		d, err := entries[i].metrics.Hash.Distance(entries[j].metrics.Hash)
		if err != nil {
			return threshold + 1 // treat as dissimilar
		}
		return d
	}
	return simgroup.Group(len(entries), threshold, distFn)
}

// buildGroup picks a winner among a similarity group's entries and
// assembles a plan.Group, content-hashing the winner and every loser
// along the way. ok is false when the group ends up with no valid
// losers to report (e.g. the winner or every loser failed to hash),
// in which case the caller should drop it.
func buildGroup(id int, entries []entry, blurThreshold float64) (g plan.Group, ok bool, warnings []Warning) {
	candidates := make([]pick.Candidate, len(entries))
	for i, e := range entries {
		candidates[i] = pick.Candidate{
			Path:      e.path,
			Sharpness: e.metrics.Sharpness,
			Width:     e.metrics.Width,
			Height:    e.metrics.Height,
			SizeBytes: e.metrics.SizeBytes,
		}
	}
	winner, losers := pick.Pick(candidates, blurThreshold)

	winnerRecord, err := toFileRecord(winner)
	if err != nil {
		return plan.Group{}, false, []Warning{{Path: winner.Path, Reason: "cannot hash file: " + err.Error()}}
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
		return plan.Group{}, false, warnings
	}

	return plan.Group{ID: id, Winner: winnerRecord, Losers: loserRecords}, true, warnings
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

	// progress is often a terminal or log write, and terminals/log
	// destinations can get noticeably slower to append to as output
	// accumulates. Calling it directly inside the critical section
	// below would mean every worker blocks on the same mutex while
	// that write happens — one slow print throttles the entire pool,
	// however fast decoding/hashing itself still is. Routing it
	// through a generously buffered channel to a single reporter
	// goroutine means a worker only ever pays the cost of a channel
	// send (fast, never blocks on I/O), while the channel's FIFO order
	// — combined with done being assigned before the send, still under
	// the lock — preserves the "one call per file, strictly
	// increasing" guarantee below exactly as before.
	type update struct {
		done int
		path string
	}
	progressCh := make(chan update, len(paths))
	reporterDone := make(chan struct{})
	go func() {
		defer close(reporterDone)
		for u := range progressCh {
			progress(u.done, len(paths), u.path)
		}
	}()

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
				d := done
				mu.Unlock()

				progressCh <- update{d, path}
			}
		}()
	}
	for _, path := range paths {
		jobs <- path
	}
	close(jobs)
	wg.Wait()
	close(progressCh)
	<-reporterDone

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

// discover returns every supported image file directly inside root,
// capped at limit (zero or negative means unlimited). The directory
// is treated as flat: subdirectories (including dedupe-kept/ and
// dedupe-quarantine/ from a prior apply) are never descended into.
// total is the number of supported files found before any cap, so a
// capped caller can report how many were left out; os.ReadDir already
// returns entries in filename order, so a repeated scan of an
// unchanged directory always caps to the same subset.
func discover(root string, limit int) (paths []string, total int, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, 0, err
	}

	paths = make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !supportedExt[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		total++
		if limit > 0 && total > limit {
			continue
		}
		paths = append(paths, filepath.Join(root, e.Name()))
	}
	return paths, total, nil
}
