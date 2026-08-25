// Command dedupe finds near-duplicate photos in a local directory,
// keeps the best one per group, and moves the rest into a same-volume
// quarantine folder — entirely locally, no network calls.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/a7madM/photo-dedupe/internal/apply"
	"github.com/a7madM/photo-dedupe/internal/plan"
	"github.com/a7madM/photo-dedupe/internal/scan"
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
  dedupe apply <plan-file>            move losers from a plan into .dedupe-quarantine/
  dedupe restore <plan-file>          move quarantined losers back to their original paths

Run 'dedupe scan -h' for scan flags.`)
}

func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	gap := fs.Duration("gap", 60*time.Second, "max gap between consecutive shots to stay in the same time-cluster")
	similarity := fs.Int("similarity", 8, "max perceptual-hash Hamming distance to treat two images as the same shot (needs tuning against your own photos)")
	blur := fs.Float64("blur", 5e6, "sharpness margin below a group's best before a candidate is excluded from winning (needs tuning against your own photos)")
	out := fs.String("out", "", "plan output path (default: <directory>/.dedupe-plan.json)")
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

	p, warnings, err := scan.Run(scan.Options{
		Root:                 root,
		GapThreshold:         *gap,
		SimilarityThreshold:  *similarity,
		BlurThreshold:        *blur,
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

	results, err := apply.Apply(p)
	if err != nil {
		return err
	}
	printResults("apply", results)
	fmt.Printf("\nquarantine folder: %s\n", filepath.Join(p.Root, apply.QuarantineDirName))
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

	results, err := apply.Restore(p)
	if err != nil {
		return err
	}
	printResults("restore", results)
	return nil
}

func readPlan(path string) (plan.Plan, error) {
	f, err := os.Open(path)
	if err != nil {
		return plan.Plan{}, err
	}
	defer f.Close()
	return plan.Read(f)
}

func printResults(action string, results []apply.Result) {
	counts := map[apply.Outcome]int{}
	for _, r := range results {
		counts[r.Outcome]++
		if r.Outcome != apply.OutcomeMoved {
			fmt.Printf("  skipped (%s): %s\n", r.Outcome, r.Path)
		}
	}
	fmt.Printf("%s complete: %d moved, %d skipped (drift), %d skipped (missing)\n",
		action, counts[apply.OutcomeMoved], counts[apply.OutcomeSkippedDrift], counts[apply.OutcomeSkippedMissing])
}
