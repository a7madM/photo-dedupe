// Package imagemetrics decodes an image and computes the signals the
// dedupe pipeline needs: a perceptual hash (for similarity grouping),
// a sharpness score (for blur-based winner filtering), pixel
// dimensions, and file size. These are the "empirical" parts of the
// tool — validated against real sample images rather than unit
// tested, since correctness here is about real-world usefulness of a
// score, not a pure deterministic contract.
package imagemetrics

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/corona10/goimagehash"
)

// Metrics holds every per-image signal the pipeline needs.
type Metrics struct {
	Width     int
	Height    int
	SizeBytes int64
	Sharpness float64
	Hash      *goimagehash.ImageHash
}

// Compute decodes the image at path and computes its Metrics.
func Compute(path string) (Metrics, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Metrics{}, err
	}

	img, err := Decode(path)
	if err != nil {
		return Metrics{}, err
	}

	hash, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return Metrics{}, err
	}

	bounds := img.Bounds()
	return Metrics{
		Width:     bounds.Dx(),
		Height:    bounds.Dy(),
		SizeBytes: info.Size(),
		Sharpness: sharpness(img),
		Hash:      hash,
	}, nil
}

var heicExt = map[string]bool{".heic": true, ".heif": true}

// Decode decodes any image format Go's stdlib understands directly
// (JPEG, PNG); HEIC/HEIF has no Go stdlib or pure-Go decoder, so those
// are decoded by shelling out to ImageMagick's "magick" binary, which
// must be on PATH and built with HEIF support for those files to
// decode. A missing/incapable magick surfaces as a decode error here,
// which Compute's caller treats like any other corrupt/unreadable
// file: skipped, logged, never a deletion candidate.
func Decode(path string) (image.Image, error) {
	if heicExt[strings.ToLower(filepath.Ext(path))] {
		return decodeHEIC(path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	return img, err
}

func decodeHEIC(path string) (image.Image, error) {
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

// sharpness scores an image using the variance of its Laplacian: a
// higher variance means more high-frequency detail (a sharper image),
// a lower variance means a flatter, blurrier image.
func sharpness(img image.Image) float64 {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 3 || h < 3 {
		return 0
	}

	gray := make([][]float64, h)
	for y := 0; y < h; y++ {
		gray[y] = make([]float64, w)
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			// Standard luminance weighting, values in 0..65535.
			gray[y][x] = 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
		}
	}

	var sum, sumSq float64
	var n float64
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			lap := gray[y-1][x] + gray[y+1][x] + gray[y][x-1] + gray[y][x+1] - 4*gray[y][x]
			sum += lap
			sumSq += lap * lap
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / n
	return sumSq/n - mean*mean
}
