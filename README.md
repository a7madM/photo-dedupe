# photo·dedupe

A local Go CLI that finds burst-shot duplicates on disk, picks the sharpest
frame, and quarantines the rest — no cloud, no accounts, nothing leaves the
machine.

## The problem

Burst mode is a lie we tell our cameras. Every genuinely good frame arrives
flanked by two or three near-identical ones, filed with equal importance,
and eventually somebody has to sit down and decide which deserves to
survive. `photo-dedupe` does that comparing for you — locally, and without
deleting anything.

## How it decides

1. **Time-cluster — `gap ≤ 20s`.** Photos are sorted by capture time (EXIF
   `DateTimeOriginal`, falling back to file mtime) and split wherever the
   gap between consecutive shots exceeds `-gap`. This only builds a
   *candidate pool* — being close in time never counts as duplicate on its
   own.
2. **Similarity-filter — `pHash distance ≤ 8`.** Within each time-cluster, a
   perceptual hash of each image is compared pairwise (`-similarity`);
   anything within the threshold is union-find'd into the same group. Two
   photos merge only because they genuinely look alike.
3. **Pick a winner — sharpness, then size.** Sharpness (variance of the
   image's Laplacian) is a filter, not a ranking — a frame more than
   `-blur` below the group's sharpest is disqualified from winning
   outright, so a big blurry photo never beats a small sharp one. Among
   what's left, resolution decides, then file size as a final tiebreak.

Resolving each file (EXIF read, decode, perceptual hash, sharpness) runs
concurrently across your CPU cores, since every file is independent of
every other — clustering and grouping still happen afterward, once every
file's resolved, so the result is the same regardless of how many cores ran
(~3.8× faster on a 150-photo sample, verified against the sequential result
with a race-detector test).

Supports JPEG, PNG, and HEIC/HEIF (RAW and Apple Photos library integration
are not in scope yet). HEIC decoding shells out to the system's `magick`
(ImageMagick) binary — Go has no stdlib or pure-Go HEIC decoder — so
`magick` must be on `PATH` and built with HEIF support (`magick identify
-list format | grep -i heic` should list it) for HEIC files to be scanned.
A missing or incapable `magick` doesn't crash the scan: those files are
just skipped and logged like any other unreadable file.

> The plan is still just a JSON file you can read. Nothing is ever deleted
> — only ever moved into a folder with your name on the decision.

## Usage

```
dedupe scan [-gap 20s] [-similarity 8] [-blur 5e6] [-limit 1000] [-out path] [-log path] <directory>
dedupe apply <plan-file>
dedupe restore <plan-file>
dedupe serve [-addr 127.0.0.1:8765]
```

`scan` flags:

| Flag          | Default                          | Meaning                                                                                     |
|---------------|-----------------------------------|-----------------------------------------------------------------------------------------------|
| `-gap`        | `20s`                             | Max gap between consecutive shots to stay in the same time-cluster.                          |
| `-similarity` | `8`                               | Max perceptual-hash Hamming distance to treat two images as the same shot.                   |
| `-blur`       | `5e6`                             | Sharpness margin below a group's best before a candidate is excluded from winning.            |
| `-limit`      | `1000`                            | Max images to process in one scan (`0` = unlimited); keeps runtime bounded on very large directories. Files beyond the cap are taken in stable filename order, so a rerun with a higher limit picks up the same subset first. |
| `-out`        | `<directory>/.dedupe-plan.json`   | Where to write the plan file.                                                                 |
| `-log`        | *(none)*                          | Also mirror progress lines (`[i/total] path`) to this file. Progress always prints to the terminal regardless of this flag. |

`scan`'s summary reports total library size and space reclaimable (both in
MB and as a % of the library), alongside images processed and groups found.

`scan` never touches your files — it writes a plan (`.dedupe-plan.json` in the
scanned directory by default) and prints a summary. Review the plan, then:

`apply` re-verifies every winner's and loser's content hash against the plan
(catches drift since the scan ran) and relocates them on the same volume —
winners into `dedupe-kept/`, losers into `dedupe-quarantine/` — preserving
each file's original relative path under whichever folder it lands in.
Nothing is ever hard-deleted: `dedupe-quarantine/` is yours to review and
delete yourself once you trust the results.

`restore` reverses an apply, moving both kept and quarantined files back to
their original paths using the same plan file.

`apply` only touches files that ended up in a group — an image with no
close-enough duplicate is never part of a group, so it's left untouched at
its original path. `dedupe-kept/` holds group winners only, not your whole
library; the rest of your files stay exactly where they were.

Re-running `scan` on the same directory always skips `dedupe-kept/` and
`dedupe-quarantine/`, so it's safe to run repeatedly.

`serve` opens a browser UI instead of using the terminal for `scan`/`apply`/
`restore` — styled like a darkroom contact sheet, the keeper gets a
grease-pencil circle and rejects get the X, the way an editor marks up a
real proof sheet. It starts with no directory selected — a form on the page
asks for one (plus `-gap`/`-similarity`/`-blur`, prefilled with the same
defaults as the CLI, with inline hints explaining each), runs the scan
server-side, and shows the gallery: each group's winner and losers side by
side (HEIC is converted to JPEG on the fly for display, since most browsers
other than Safari can't render it natively), with Apply/Restore buttons
wired to the same logic as the CLI commands, plus the ability to override
a group's winner before applying and see quarantine stats afterward. It
only binds to the loopback address you give it (`127.0.0.1` by default) and
only ever serves images that are actually part of the currently loaded
plan — nothing else on disk is reachable through it. Scanning a different
directory from the form at any time replaces the loaded plan.

## Testing against a sample

Before pointing `scan` at a whole library, it's worth trying it against a
small sample first. This copies the 100 most-recently-modified supported
images (jpg/jpeg/png/heic/heif) from a source directory into a sample
folder, skipping macOS's `._*` AppleDouble sidecar files:

```
mkdir -p ~/Documents/test-dedupe

find "<source directory>" -type f \
  \( -iname '*.jpg' -o -iname '*.jpeg' -o -iname '*.png' -o -iname '*.heic' -o -iname '*.heif' \) \
  ! -name '._*' \
  -exec stat -f '%m %N' {} \; \
  | sort -rn \
  | head -100 \
  | cut -d' ' -f2- \
  | while IFS= read -r f; do cp -p "$f" ~/Documents/test-dedupe/; done

ls ~/Documents/test-dedupe | wc -l
```

How it works:

- `stat -f '%m %N'` prefixes each path with its modification time (epoch
  seconds).
- `sort -rn` sorts numerically descending, so the newest files come first.
- `head -100` takes the top 100.
- `cut -d' ' -f2-` strips the mtime prefix back off — safe even with spaces
  in filenames, since it just rejoins everything after the first field.
- `cp -p` preserves the original mtime on the copies, so `scan`'s
  time-clustering fallback (EXIF-less files) behaves the same on the sample
  as it would on the source.

Adjust `head -100` and `~/Documents/test-dedupe` to change the sample size or
destination. Then run `scan` against the sample folder as usual.

## Tuning

`-similarity` and `-blur` are the two knobs most likely to need adjustment
against your actual photo library — the defaults are starting points, not
validated thresholds. Run `scan`, inspect `.dedupe-plan.json`, and adjust
before ever running `apply`.

## Architecture

The scan pipeline is ten single-purpose packages, run in a straight line:

| Package        | Role                                                                     |
|----------------|---------------------------------------------------------------------------|
| `exiftime`     | Resolves a capture timestamp: EXIF first, file mtime otherwise.          |
| `cluster`      | Gap-based time clustering over sorted timestamps.                        |
| `imagemetrics` | Decode, perceptual hash, sharpness, dimensions — HEIC via ImageMagick.   |
| `simgroup`     | Union-find over hash distance — generic connected components.            |
| `pick`         | Chooses a group's winner: sharpness, resolution, size.                   |
| `plan`         | The on-disk JSON schema a scan produces and apply reads back.            |
| `filehash`     | SHA-256, shared by scan's recording and apply's drift check.             |
| `apply`        | The only package that moves files — and reverses itself.                 |
| `scan`         | Orchestrates the whole pipeline, over a worker pool.                     |
| `webui`        | The loopback-only browser front end for all of the above.                |

See `CLAUDE.md` for the full pipeline walkthrough.

## Status

Core pipeline built and tested (`go test ./...`). Not yet validated against a
real photo library — do a `scan` (dry-run) against real files and inspect the
plan before trusting `apply`.
