package scan

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/a7madM/photo-dedupe/internal/apply"
)

// heicFixture builds a synthetic HEIC file at path from img by
// shelling out to the system's `magick` binary (mirrors
// internal/imagemetrics's test fixture helper). Skips rather than
// fails when magick isn't available.
func heicFixture(t *testing.T, path string, img image.Image, mtime time.Time) {
	t.Helper()
	if _, err := exec.LookPath("magick"); err != nil {
		t.Skip("magick not on PATH; skipping HEIC test")
	}

	pngPath := path + ".src.png"
	writePNG(t, pngPath, img, mtime)

	cmd := exec.Command("magick", pngPath, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("magick heic conversion failed: %v: %s", err, out)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("setting mtime: %v", err)
	}
	if err := os.Remove(pngPath); err != nil {
		t.Fatalf("removing intermediate png: %v", err)
	}
}

func writePNG(t *testing.T, path string, img image.Image, mtime time.Time) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatalf("encode fixture png: %v", err)
	}
	f.Close()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("setting mtime: %v", err)
	}
}

func checkerboard(size int) image.Image {
	img := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if (x/4+y/4)%2 == 0 {
				img.SetGray(x, y, color.Gray{Y: 255})
			} else {
				img.SetGray(x, y, color.Gray{Y: 0})
			}
		}
	}
	return img
}

// quadrants draws four large solid-color blocks — distinct enough for
// a meaningful perceptual hash but not adversarial to HEIC's
// block-based lossy compression, unlike a fine checkerboard.
func quadrants(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	colors := [4]color.RGBA{
		{R: 220, G: 40, B: 40, A: 255},
		{R: 40, G: 200, B: 60, A: 255},
		{R: 40, G: 80, B: 220, A: 255},
		{R: 230, G: 200, B: 30, A: 255},
	}
	half := size / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			idx := 0
			if x >= half {
				idx += 1
			}
			if y >= half {
				idx += 2
			}
			img.SetRGBA(x, y, colors[idx])
		}
	}
	return img
}

func flat(size int) image.Image {
	img := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetGray(x, y, color.Gray{Y: 128})
		}
	}
	return img
}

func TestRun_ReportsProgressForEachDiscoveredFile(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	writePNG(t, filepath.Join(root, "a.png"), checkerboard(64), base)
	writePNG(t, filepath.Join(root, "b.png"), flat(64), base.Add(time.Hour))

	type call struct {
		index, total int
		path         string
	}
	var calls []call

	_, _, err := Run(Options{
		Root:                 root,
		GapThreshold:         time.Minute,
		SimilarityThreshold:  8,
		BlurThreshold:        1e9,
		Progress: func(index, total int, path string) {
			calls = append(calls, call{index, total, path})
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("progress calls = %+v, want 2 calls (one per discovered file)", calls)
	}
	for i, c := range calls {
		if c.index != i+1 {
			t.Fatalf("calls[%d].index = %d, want %d", i, c.index, i+1)
		}
		if c.total != 2 {
			t.Fatalf("calls[%d].total = %d, want 2", i, c.total)
		}
		if c.path == "" {
			t.Fatalf("calls[%d].path is empty", i)
		}
	}
}

func TestRun_NilProgressIsOptional(t *testing.T) {
	root := t.TempDir()
	writePNG(t, filepath.Join(root, "a.png"), checkerboard(64), time.Now())

	if _, _, err := Run(Options{Root: root, GapThreshold: time.Minute, SimilarityThreshold: 8, BlurThreshold: 1e9}); err != nil {
		t.Fatalf("Run with nil Progress returned error: %v", err)
	}
}

func TestRun_GroupsDuplicatesWithinTimeAndSimilarity(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// img1 and img2: identical content, a few seconds apart -> should be grouped.
	writePNG(t, filepath.Join(root, "img1.png"), checkerboard(64), base)
	writePNG(t, filepath.Join(root, "img2.png"), checkerboard(64), base.Add(5*time.Second))

	// img3: different content, same time window -> similar time, not similar image.
	writePNG(t, filepath.Join(root, "img3.png"), flat(64), base.Add(10*time.Second))

	// img4: identical content to img1/img2, but far outside the time gap.
	writePNG(t, filepath.Join(root, "img4.png"), checkerboard(64), base.Add(2*time.Hour))

	p, warnings, err := Run(Options{
		Root:                 root,
		GapThreshold:         time.Minute,
		SimilarityThreshold:  8,
		BlurThreshold:        1e9, // sharpness is identical for these synthetic fixtures
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}

	if len(p.Groups) != 1 {
		t.Fatalf("Groups = %+v, want exactly 1 group", p.Groups)
	}
	g := p.Groups[0]
	if len(g.Losers) != 1 {
		t.Fatalf("Losers = %+v, want exactly 1", g.Losers)
	}

	members := map[string]bool{g.Winner.Path: true}
	for _, l := range g.Losers {
		members[l.Path] = true
	}
	img1 := filepath.Join(root, "img1.png")
	img2 := filepath.Join(root, "img2.png")
	img3 := filepath.Join(root, "img3.png")
	img4 := filepath.Join(root, "img4.png")

	if !members[img1] || !members[img2] {
		t.Fatalf("group members = %v, want img1 and img2 present", members)
	}
	if members[img3] {
		t.Fatalf("group members = %v, want img3 (dissimilar) absent", members)
	}
	if members[img4] {
		t.Fatalf("group members = %v, want img4 (outside time gap) absent", members)
	}
}

func TestRun_GroupsAcrossHEICAndPNG(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// Same shot, different formats (e.g. iPhone HEIC original plus a
	// PNG export) a few seconds apart -> should still group together.
	writePNG(t, filepath.Join(root, "img1.png"), quadrants(64), base)
	heicFixture(t, filepath.Join(root, "img2.heic"), quadrants(64), base.Add(5*time.Second))

	// Distinct content, same time window -> must not join the group.
	writePNG(t, filepath.Join(root, "img3.png"), flat(64), base.Add(10*time.Second))

	p, warnings, err := Run(Options{
		Root:                root,
		GapThreshold:        time.Minute,
		SimilarityThreshold: 8,
		BlurThreshold:       1e9,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(p.Groups) != 1 {
		t.Fatalf("Groups = %+v, want exactly 1 group", p.Groups)
	}

	g := p.Groups[0]
	members := map[string]bool{g.Winner.Path: true}
	for _, l := range g.Losers {
		members[l.Path] = true
	}
	if !members[filepath.Join(root, "img1.png")] || !members[filepath.Join(root, "img2.heic")] {
		t.Fatalf("group members = %v, want img1.png and img2.heic present", members)
	}
	if members[filepath.Join(root, "img3.png")] {
		t.Fatalf("group members = %v, want img3.png (dissimilar) absent", members)
	}
}

func TestRun_ExcludesQuarantineDirectory(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	quarantineDir := filepath.Join(root, apply.QuarantineDirName)
	if err := os.MkdirAll(quarantineDir, 0o755); err != nil {
		t.Fatalf("mkdir quarantine: %v", err)
	}
	writePNG(t, filepath.Join(quarantineDir, "old-loser.png"), checkerboard(64), base)

	p, _, err := Run(Options{Root: root, GapThreshold: time.Minute, SimilarityThreshold: 8, BlurThreshold: 1e9})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(p.Groups) != 0 {
		t.Fatalf("Groups = %+v, want none (only file was in quarantine dir)", p.Groups)
	}
}

func TestRun_ExcludesKeptDirectory(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	keptDir := filepath.Join(root, apply.KeptDirName)
	if err := os.MkdirAll(keptDir, 0o755); err != nil {
		t.Fatalf("mkdir kept: %v", err)
	}
	// Two identical, close-in-time images: if the kept dir weren't
	// excluded, these would cluster and similarity-group with each
	// other, producing a Group. A single file wouldn't prove exclusion
	// either way, since it can never form a group on its own.
	writePNG(t, filepath.Join(keptDir, "old-winner-1.png"), checkerboard(64), base)
	writePNG(t, filepath.Join(keptDir, "old-winner-2.png"), checkerboard(64), base.Add(time.Second))

	p, _, err := Run(Options{Root: root, GapThreshold: time.Minute, SimilarityThreshold: 8, BlurThreshold: 1e9})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(p.Groups) != 0 {
		t.Fatalf("Groups = %+v, want none (only file was in kept dir)", p.Groups)
	}
}
