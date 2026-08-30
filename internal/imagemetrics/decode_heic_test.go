package imagemetrics

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// decodeHEICViaPNG replicates decodeHEIC's implementation before the
// PPM switch (magick ... png:- piped into image.Decode). Kept only in
// this test file as the reference the PPM path must match exactly —
// see TestDecodeHEIC_MatchesOldPNGPath.
func decodeHEICViaPNG(path string) (image.Image, error) {
	cmd := exec.Command("magick", path, "png:-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("magick decode of %s failed: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	img, _, err := image.Decode(&stdout)
	return img, err
}

// TestDecodeHEIC_MatchesOldPNGPath is the correctness guarantee the
// PPM optimization depends on: decodeHEIC's new magick-ppm-then-parse
// path and the old magick-png-then-image.Decode path both feed off
// the exact same magick-decoded pixels, just through a different
// (both lossless) container, so every pixel must come back identical.
func TestDecodeHEIC_MatchesOldPNGPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.heic")
	heicFixture(t, path, quadrants(96))

	got, err := decodeHEIC(path)
	if err != nil {
		t.Fatalf("decodeHEIC returned error: %v", err)
	}
	want, err := decodeHEICViaPNG(path)
	if err != nil {
		t.Fatalf("decodeHEICViaPNG returned error: %v", err)
	}

	gb, wb := got.Bounds(), want.Bounds()
	if gb != wb {
		t.Fatalf("bounds = %v, want %v", gb, wb)
	}
	for y := gb.Min.Y; y < gb.Max.Y; y++ {
		for x := gb.Min.X; x < gb.Max.X; x++ {
			gr, gg, gbl, ga := got.At(x, y).RGBA()
			wr, wg, wbl, wa := want.At(x, y).RGBA()
			if gr != wr || gg != wg || gbl != wbl || ga != wa {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y,
					color.RGBA64{R: uint16(gr), G: uint16(gg), B: uint16(gbl), A: uint16(ga)},
					color.RGBA64{R: uint16(wr), G: uint16(wg), B: uint16(wbl), A: uint16(wa)})
			}
		}
	}
}

func TestDecodePPM_ParsesHeaderAndPixels(t *testing.T) {
	// A tiny 2x1 P6 image: one red pixel, one green pixel.
	raw := []byte("P6\n2 1\n255\n\xff\x00\x00\x00\xff\x00")

	img, err := decodePPM(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decodePPM returned error: %v", err)
	}
	if img.Bounds() != image.Rect(0, 0, 2, 1) {
		t.Fatalf("bounds = %v, want (0,0)-(2,1)", img.Bounds())
	}
	if got := img.NRGBAAt(0, 0); got != (color.NRGBA{R: 255, G: 0, B: 0, A: 255}) {
		t.Fatalf("pixel (0,0) = %v, want opaque red", got)
	}
	if got := img.NRGBAAt(1, 0); got != (color.NRGBA{R: 0, G: 255, B: 0, A: 255}) {
		t.Fatalf("pixel (1,0) = %v, want opaque green", got)
	}
}

// TestDecodePPM_ToleratesCommentsAndExtraWhitespace exercises the PNM
// spec's comment/whitespace rules that magick's own output never
// happens to use, so a decoder written only against magick's exact
// output today would be a latent break waiting for a future magick
// version that formats the header differently.
func TestDecodePPM_ToleratesCommentsAndExtraWhitespace(t *testing.T) {
	raw := []byte("P6 # a comment\n  2   1\n255\n\x10\x20\x30\x40\x50\x60")

	img, err := decodePPM(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decodePPM returned error: %v", err)
	}
	if got := img.NRGBAAt(0, 0); got != (color.NRGBA{R: 0x10, G: 0x20, B: 0x30, A: 255}) {
		t.Fatalf("pixel (0,0) = %v, want {10 20 30 ff}", got)
	}
}

func TestDecodePPM_RejectsWrongMagic(t *testing.T) {
	_, err := decodePPM(bytes.NewReader([]byte("P5\n2 1\n255\n\x00\x00")))
	if err == nil {
		t.Fatal("expected an error for a non-P6 magic, got nil")
	}
}

func TestDecodePPM_RejectsNonByteMaxval(t *testing.T) {
	_, err := decodePPM(bytes.NewReader([]byte("P6\n2 1\n65535\n")))
	if err == nil {
		t.Fatal("expected an error for a maxval other than 255, got nil")
	}
}

func TestDecodePPM_RejectsTruncatedPixelData(t *testing.T) {
	// Header declares 2x2 pixels (12 bytes) but only 3 are present.
	_, err := decodePPM(bytes.NewReader([]byte("P6\n2 2\n255\n\x01\x02\x03")))
	if err == nil {
		t.Fatal("expected an error for truncated pixel data, got nil")
	}
}

func TestDecodePPM_RejectsOversizedDimensions(t *testing.T) {
	_, err := decodePPM(bytes.NewReader([]byte("P6\n999999999 1\n255\n")))
	if err == nil {
		t.Fatal("expected an error for a dimension over maxPPMDimension, got nil")
	}
}
