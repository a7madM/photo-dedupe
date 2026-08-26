# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A local CLI (and optional local web UI) that finds near-duplicate photos
(e.g. burst shots) in a directory, picks the best image per duplicate group,
and moves losers into a quarantine folder for the user to review and delete
themselves. Fully local — no network calls, nothing is ever hard-deleted.

## Commands

```
go build -o dedupe ./cmd/dedupe   # build the CLI binary
go test ./...                     # run all tests
go test ./internal/pick/...       # run a single package's tests
go test ./internal/pick/ -run TestPick -v   # run a single test
go vet ./...
```

Running the tool itself:

```
./dedupe scan [-gap 60s] [-similarity 8] [-blur 5e6] [-out path] [-log path] <directory>
./dedupe apply <plan-file>
./dedupe restore <plan-file>
./dedupe serve [-addr 127.0.0.1:8765]
```

`scan` is a dry run only — it writes a `.dedupe-plan.json` plan and never
touches files. `apply`/`restore` are the only commands that move files on
disk, and both operate off a plan file, not the directory directly.

HEIC/HEIF decoding shells out to the system `magick` (ImageMagick) binary —
Go has no stdlib or pure-Go HEIC decoder. It must be on `PATH` and built with
HEIF support (`magick identify -list format | grep -i heic`). Its absence
never crashes a scan; affected files are just skipped and logged as warnings.

## Architecture

The scan pipeline (`internal/scan`) is a straight-line orchestration over
single-purpose packages, run in this order for every discovered file, then
per time-cluster, then per similarity-group:

1. **`exiftime`** — resolve each file's capture timestamp: EXIF
   `DateTimeOriginal`, falling back to file mtime.
2. **`imagemetrics`** — decode the image (dispatching HEIC/HEIF to `magick`)
   and compute a perceptual hash (`goimagehash`), a sharpness score
   (variance of the Laplacian), pixel dimensions, and file size.
3. **`cluster`** — gap-based time clustering: consecutive shots (sorted by
   timestamp) stay in one cluster as long as the gap between them is within
   `-gap`. Pure time proximity never implies duplication by itself.
4. **`simgroup`** — within a time-cluster, union-find over perceptual-hash
   Hamming distance (`-similarity` threshold) splits it into actual
   similarity groups. This is generic connected-components logic; the
   distance function is injected by the caller.
5. **`pick`** — within a similarity group, choose the winner: sharpness is
   an eligibility filter (a candidate more than `-blur` below the group's
   best sharpness is excluded from winning, not merely penalized), then
   highest resolution, then largest file size, then lexicographic path as a
   final deterministic tiebreak.
6. **`plan`** — the JSON schema (`.dedupe-plan.json`) that scan output is
   serialized to and that `apply`/`restore` read back. Each group's
   `FileRecord` carries a SHA-256 `ContentHash` used later to detect drift.
7. **`apply`** — the only package that mutates the filesystem. `Apply`
   re-hashes every winner/loser against the plan's recorded `ContentHash`
   before moving it (a mismatch or missing file is skipped and reported,
   never erroring the whole run); winners move into `dedupe-kept/`, losers
   into `dedupe-quarantine/`, both under the plan's root, preserving each
   file's original relative path. `Restore` reverses this using the same
   plan file. Nothing is ever deleted by this tool.

`internal/filehash` (SHA-256) is shared by `scan` (recording hashes into the
plan) and `apply` (drift detection before every move).

`internal/webui` wraps the same `scan`/`apply`/`restore` calls behind an
`http.Handler` (`cmd/dedupe` mounts it via `serve`): a single in-memory
`Server` holds the currently loaded plan behind a mutex, a directory-picker
form re-runs `scan.Run` and replaces that plan, and `/image` is the only
other route — it re-encodes HEIC to JPEG on the fly and only ever serves
paths present in the loaded plan's winner/loser set, nothing else on disk.

`cmd/dedupe/main.go` is a thin flag-parsing/formatting layer over these
packages — `scan`/`apply`/`restore`/`serve` subcommands each just build
options, call into the corresponding package, and print/format the result.

### Directory safety invariant

`dedupe-kept/` and `dedupe-quarantine/` (constants `apply.KeptDirName`,
`apply.QuarantineDirName`) are always excluded by `scan`'s directory walk.
This is what makes re-running `scan` on the same root after an `apply` safe
and idempotent — don't remove that exclusion without preserving the
invariant some other way.

## Notes on testing style

`internal/imagemetrics` has no unit tests by design — sharpness/perceptual-hash
scores are validated empirically against real sample images (see README
"Testing against a sample"), not asserted as a deterministic contract. Every
other package (`cluster`, `simgroup`, `pick`, `plan`, `apply`, `exiftime`,
`filehash`, `webui`) has standard Go table-driven tests.
