package imagemetrics

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// heicFixture builds a synthetic HEIC file at path by encoding img as
// PNG and converting it with the system's `magick` binary. Tests
// using this skip (rather than fail) when magick isn't available,
// since HEIC support is an optional runtime dependency, not a Go
// stdlib capability.
func heicFixture(t *testing.T, path string, img image.Image) {
	t.Helper()
	if _, err := exec.LookPath("magick"); err != nil {
		t.Skip("magick not on PATH; skipping HEIC test")
	}

	pngPath := path + ".src.png"
	writePNG(t, pngPath, img)

	cmd := exec.Command("magick", pngPath, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("magick heic conversion failed: %v: %s", err, out)
	}
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode fixture png: %v", err)
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

// quadrants draws four large solid-color blocks — visually distinct
// enough for a meaningful perceptual hash, but (unlike a fine
// checkerboard) not adversarial to block-based lossy compression like
// HEIC's, so it's suitable for cross-format hash comparisons.
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

func TestCompute_ValidImage_ReturnsDimensionsAndSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.png")
	writePNG(t, path, checkerboard(64))

	m, err := Compute(path)
	if err != nil {
		t.Fatalf("Compute returned error: %v", err)
	}
	if m.Width != 64 || m.Height != 64 {
		t.Fatalf("dimensions = %dx%d, want 64x64", m.Width, m.Height)
	}
	if m.SizeBytes <= 0 {
		t.Fatalf("SizeBytes = %d, want > 0", m.SizeBytes)
	}
	if m.Hash == nil {
		t.Fatal("Hash is nil, want a computed perceptual hash")
	}
}

func TestCompute_IdenticalImages_HashDistanceZero(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	b := filepath.Join(dir, "b.png")
	writePNG(t, a, checkerboard(64))
	writePNG(t, b, checkerboard(64))

	ma, err := Compute(a)
	if err != nil {
		t.Fatalf("Compute(a) error: %v", err)
	}
	mb, err := Compute(b)
	if err != nil {
		t.Fatalf("Compute(b) error: %v", err)
	}

	dist, err := ma.Hash.Distance(mb.Hash)
	if err != nil {
		t.Fatalf("Distance error: %v", err)
	}
	if dist != 0 {
		t.Fatalf("distance between identical images = %d, want 0", dist)
	}
}

func TestCompute_DetailedImage_ScoresSharperThanFlatImage(t *testing.T) {
	dir := t.TempDir()
	sharpPath := filepath.Join(dir, "sharp.png")
	flatPath := filepath.Join(dir, "flat.png")
	writePNG(t, sharpPath, checkerboard(64))
	writePNG(t, flatPath, flat(64))

	sharp, err := Compute(sharpPath)
	if err != nil {
		t.Fatalf("Compute(sharp) error: %v", err)
	}
	flatM, err := Compute(flatPath)
	if err != nil {
		t.Fatalf("Compute(flat) error: %v", err)
	}

	if sharp.Sharpness <= flatM.Sharpness {
		t.Fatalf("sharp.Sharpness = %v, flat.Sharpness = %v; want sharp > flat", sharp.Sharpness, flatM.Sharpness)
	}
}

func TestCompute_HEIC_DecodesAndComputesMetrics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.heic")
	heicFixture(t, path, checkerboard(64))

	m, err := Compute(path)
	if err != nil {
		t.Fatalf("Compute(heic) returned error: %v", err)
	}
	if m.Width != 64 || m.Height != 64 {
		t.Fatalf("dimensions = %dx%d, want 64x64", m.Width, m.Height)
	}
	if m.Hash == nil {
		t.Fatal("Hash is nil, want a computed perceptual hash")
	}
	// SizeBytes must reflect the actual .heic file on disk, not the
	// intermediate PNG used to decode it.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}
	if m.SizeBytes != info.Size() {
		t.Fatalf("SizeBytes = %d, want %d (the .heic file's own size)", m.SizeBytes, info.Size())
	}
}

func TestCompute_HEICAndPNGOfSameImage_HashesMatchClosely(t *testing.T) {
	dir := t.TempDir()
	heicPath := filepath.Join(dir, "a.heic")
	pngPath := filepath.Join(dir, "a.png")
	heicFixture(t, heicPath, quadrants(64))
	writePNG(t, pngPath, quadrants(64))

	heicM, err := Compute(heicPath)
	if err != nil {
		t.Fatalf("Compute(heic) error: %v", err)
	}
	pngM, err := Compute(pngPath)
	if err != nil {
		t.Fatalf("Compute(png) error: %v", err)
	}

	dist, err := heicM.Hash.Distance(pngM.Hash)
	if err != nil {
		t.Fatalf("Distance error: %v", err)
	}
	// HEIC's lossy compression means this won't be byte-identical to
	// the PNG source, but the perceptual hash should still land close.
	if dist > 8 {
		t.Fatalf("hash distance between HEIC and PNG of the same image = %d, want <= 8", dist)
	}
}

func TestCompute_MissingFile_ReturnsError(t *testing.T) {
	_, err := Compute(filepath.Join(t.TempDir(), "missing.png"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestCompute_CorruptFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.png")
	if err := os.WriteFile(path, []byte("not a real png"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	_, err := Compute(path)
	if err == nil {
		t.Fatal("expected error for corrupt file, got nil")
	}
}
