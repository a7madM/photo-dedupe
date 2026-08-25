# photo-dedupe

Finds near-duplicate photos in a local directory (e.g. burst shots), keeps the
best one per group, and moves the rest to a quarantine folder — fully local,
no network calls.

## How it works

1. **Time-cluster**: images are grouped by capture time (EXIF `DateTimeOriginal`,
   falling back to file mtime) using gap-based clustering — a run of shots stays
   in one cluster as long as consecutive shots are within `-gap` of each other.
2. **Similarity-filter**: within each time cluster, perceptual hashing (pHash)
   decides which images are actually the same shot (`-similarity`, max Hamming
   distance). Time-proximity alone never merges dissimilar shots.
3. **Pick a winner**: within each similarity group — sharpness first (a
   candidate more than `-blur` below the group's best is excluded from
   winning), then highest resolution, then largest file size as a tiebreak.

Supports JPEG, PNG, and HEIC/HEIF (RAW and Apple Photos library integration are
not in scope yet). HEIC decoding shells out to the system's `magick`
(ImageMagick) binary — Go has no stdlib or pure-Go HEIC decoder — so `magick`
must be on `PATH` and built with HEIF support (`magick identify -list format
| grep -i heic` should list it) for HEIC files to be scanned. A missing or
incapable `magick` doesn't crash the scan: those files are just skipped and
logged like any other unreadable file.

## Usage

```
dedupe scan [-gap 60s] [-similarity 8] [-blur 5e6] [-out path] <directory>
dedupe apply <plan-file>
dedupe restore <plan-file>
```

`scan` never touches your files — it writes a plan (`.dedupe-plan.json` in the
scanned directory by default) and prints a summary. Review the plan, then:

`apply` re-verifies each loser's content hash against the plan (catches drift
since the scan ran) and moves it into `.dedupe-quarantine/` on the same
volume — never a hard delete. Empty that folder yourself once you trust the
results.

`restore` reverses an apply, moving quarantined files back to their original
paths using the same plan file.

Re-running `scan` on the same directory always skips `.dedupe-quarantine/`,
so it's safe to run repeatedly.

## Tuning

`-similarity` and `-blur` are the two knobs most likely to need adjustment
against your actual photo library — the defaults are starting points, not
validated thresholds. Run `scan`, inspect `.dedupe-plan.json`, and adjust
before ever running `apply`.

## Status

Core pipeline built and tested (`go test ./...`). Not yet validated against a
real photo library — do a `scan` (dry-run) against real files and inspect the
plan before trusting `apply`.
