// Command dedupe finds near-duplicate photos in a local directory,
// keeps the best one per group, and moves the rest into a same-volume
// quarantine folder — entirely locally, no network calls.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/a7madM/photo-dedupe/internal/apply"
	"github.com/a7madM/photo-dedupe/internal/plan"
	"github.com/a7madM/photo-dedupe/internal/scan"
	"github.com/a7madM/photo-dedupe/internal/webui"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "scan":
		err = runScan(os.Args[2:])
	case "apply":
		err = runApply(os.Args[2:])
	case "restore":
		err = runRestore(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `dedupe — find and quarantine near-duplicate photos, fully locally.

Usage:
  dedupe scan [flags] <directory>     scan a directory, write a plan (dry-run; no files touched)
  dedupe apply <plan-file>            sort a plan's winners into dedupe-kept/ and losers into dedupe-quarantine/
  dedupe restore <plan-file>          move kept and quarantined files back to their original paths
  dedupe serve [-addr host:port]      open a browser UI; pick a directory, scan, apply/restore there

Run 'dedupe scan -h' or 'dedupe serve -h' for flags.`)
}

func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	gap := fs.Duration("gap", 60*time.Second, "max gap between consecutive shots to stay in the same time-cluster")
	similarity := fs.Int("similarity", 8, "max perceptual-hash Hamming distance to treat two images as the same shot (needs tuning against your own photos)")
	blur := fs.Float64("blur", 5e6, "sharpness margin below a group's best before a candidate is excluded from winning (needs tuning against your own photos)")
	out := fs.String("out", "", "plan output path (default: <directory>/.dedupe-plan.json)")
	logPath := fs.String("log", "", "also write progress logs to this file (progress always prints to the terminal)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: dedupe scan [flags] <directory>")
	}
	root, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}

	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(root, ".dedupe-plan.json")
	}

	progressOut := io.Writer(os.Stderr)
	if *logPath != "" {
		logFile, err := os.Create(*logPath)
		if err != nil {
			return fmt.Errorf("creating log file: %w", err)
		}
		defer logFile.Close()
		progressOut = io.MultiWriter(os.Stderr, logFile)
	}

	p, warnings, err := scan.Run(scan.Options{
		Root:                root,
		GapThreshold:        *gap,
		SimilarityThreshold: *similarity,
		BlurThreshold:       *blur,
		Progress: func(index, total int, path string) {
			fmt.Fprintf(progressOut, "[%d/%d] %s\n", index, total, path)
		},
	})
	if err != nil {
		return err
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := plan.Write(f, p); err != nil {
		return err
	}

	var loserCount int
	var spaceBytes int64
	for _, g := range p.Groups {
		loserCount += len(g.Losers)
		for _, l := range g.Losers {
			spaceBytes += l.SizeBytes
		}
	}

	fmt.Printf("scanned %s\n", root)
	fmt.Printf("images processed:    %d\n", p.Stats.TotalImages)
	fmt.Printf("processing time:     %s\n", formatStats(p.Stats))
	fmt.Printf("groups found:        %d\n", len(p.Groups))
	fmt.Printf("files to quarantine: %d\n", loserCount)
	fmt.Printf("space reclaimable:   %.1f MB\n", float64(spaceBytes)/1e6)
	fmt.Printf("plan written to:     %s\n", outPath)
	if len(warnings) > 0 {
		fmt.Printf("skipped %d file(s) (never treated as deletion candidates):\n", len(warnings))
		for _, w := range warnings {
			fmt.Printf("  - %s: %s\n", w.Path, w.Reason)
		}
	}
	fmt.Println("\nthis was a dry run — review the plan, then run: dedupe apply", outPath)
	return nil
}

func runApply(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: dedupe apply <plan-file>")
	}
	p, err := readPlan(args[0])
	if err != nil {
		return err
	}

	start := time.Now()
	results, err := apply.Apply(p)
	if err != nil {
		return err
	}
	printResults("apply", results, time.Since(start))
	fmt.Printf("\nkept folder:        %s\n", filepath.Join(p.Root, apply.KeptDirName))
	fmt.Printf("quarantine folder:  %s (review, then delete it yourself)\n", filepath.Join(p.Root, apply.QuarantineDirName))
	fmt.Println("to undo, run: dedupe restore", args[0])
	return nil
}

func runRestore(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: dedupe restore <plan-file>")
	}
	p, err := readPlan(args[0])
	if err != nil {
		return err
	}

	start := time.Now()
	results, err := apply.Restore(p)
	if err != nil {
		return err
	}
	printResults("restore", results, time.Since(start))
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8765", "address to bind the local web UI to (loopback only by default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: dedupe serve [-addr host:port]")
	}

	url := "http://" + *addr + "/"
	fmt.Println("open", url)
	openBrowser(url)

	return http.ListenAndServe(*addr, webui.New())
}

// openBrowser best-effort opens url in the default browser; failure
// is silent since the printed URL above is always the fallback.
func openBrowser(url string) {
	if runtime.GOOS != "darwin" {
		return
	}
	_ = exec.Command("open", url).Start()
}

func readPlan(path string) (plan.Plan, error) {
	f, err := os.Open(path)
	if err != nil {
		return plan.Plan{}, err
	}
	defer f.Close()
	return plan.Read(f)
}

func printResults(action string, results []apply.Result, elapsed time.Duration) {
	var movedWinners, movedLosers int
	counts := map[apply.Outcome]int{}
	for _, r := range results {
		counts[r.Outcome]++
		switch {
		case r.Outcome == apply.OutcomeMoved && r.Role == apply.RoleWinner:
			movedWinners++
		case r.Outcome == apply.OutcomeMoved && r.Role == apply.RoleLoser:
			movedLosers++
		case r.Outcome != apply.OutcomeMoved:
			fmt.Printf("  skipped (%s, %s): %s\n", r.Outcome, r.Role, r.Path)
		}
	}
	fmt.Printf("%s complete: %d winners moved, %d losers moved, %d skipped (drift), %d skipped (missing)\n",
		action, movedWinners, movedLosers, counts[apply.OutcomeSkippedDrift], counts[apply.OutcomeSkippedMissing])
	fmt.Printf("%d file(s) processed in %s\n", len(results), elapsed.Round(10*time.Millisecond))
}

// formatStats renders a scan's performance figures as "3.4s (12.3
// images/sec)", or just the duration when there's nothing to divide by.
func formatStats(s plan.Stats) string {
	d := time.Duration(s.DurationMS) * time.Millisecond
	if s.TotalImages == 0 || s.DurationMS == 0 {
		return d.Round(10 * time.Millisecond).String()
	}
	rate := float64(s.TotalImages) / (float64(s.DurationMS) / 1000)
	return fmt.Sprintf("%s (%.1f images/sec)", d.Round(10*time.Millisecond), rate)
}
