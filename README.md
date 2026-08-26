# photo-dedupe

Finds near-duplicate photos in a local directory (e.g. burst shots), and on
`apply` sorts each group's best image into a kept folder and the rest into a
quarantine folder for you to review and delete yourself — fully local, no
network calls.

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

Resolving each file (EXIF read, image decode, perceptual hash, sharpness)
runs concurrently across your CPU cores, since every file is independent of
every other — clustering and grouping still happen afterward, once every
file's resolved, so the result is the same regardless of how many cores ran.

Supports JPEG, PNG, and HEIC/HEIF (RAW and Apple Photos library integration are
not in scope yet). HEIC decoding shells out to the system's `magick`
(ImageMagick) binary — Go has no stdlib or pure-Go HEIC decoder — so `magick`
must be on `PATH` and built with HEIF support (`magick identify -list format
| grep -i heic` should list it) for HEIC files to be scanned. A missing or
incapable `magick` doesn't crash the scan: those files are just skipped and
logged like any other unreadable file.

## Usage

```
dedupe scan [-gap 60s] [-similarity 8] [-blur 5e6] [-out path] [-log path] <directory>
dedupe apply <plan-file>
dedupe restore <plan-file>
dedupe serve [-addr 127.0.0.1:8765]
```

`scan` flags:

| Flag          | Default                          | Meaning                                                                                     |
|---------------|-----------------------------------|-----------------------------------------------------------------------------------------------|
| `-gap`        | `60s`                             | Max gap between consecutive shots to stay in the same time-cluster.                          |
| `-similarity` | `8`                               | Max perceptual-hash Hamming distance to treat two images as the same shot.                   |
| `-blur`       | `5e6`                             | Sharpness margin below a group's best before a candidate is excluded from winning.            |
| `-out`        | `<directory>/.dedupe-plan.json`   | Where to write the plan file.                                                                 |
| `-log`        | *(none)*                          | Also mirror progress lines (`[i/total] path`) to this file. Progress always prints to the terminal regardless of this flag. |

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
`restore`. It starts with no directory selected — a form on the page asks
for one (plus `-gap`/`-similarity`/`-blur`, prefilled with the same defaults
as the CLI), runs the scan server-side, and shows the gallery: each group's
winner and losers side by side (HEIC is converted to JPEG on the fly for
display, since most browsers other than Safari can't render it natively),
with Apply/Restore buttons wired to the same logic as the CLI commands. It
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

## Status

Core pipeline built and tested (`go test ./...`). Not yet validated against a
real photo library — do a `scan` (dry-run) against real files and inspect the
plan before trusting `apply`.
