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
	// magick's PNG output is typically several times the HEIC input's
	// size; sizing the buffer off the source file avoids the repeated
	// grow-and-copy a from-empty bytes.Buffer would do while stdout
	// streams in.
	if info, err := os.Stat(path); err == nil {
		stdout.Grow(int(info.Size()) * 4)
	}
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

	gray := grayscale(img, bounds, w, h)

	var sum, sumSq float64
	var n float64
	for y := 1; y < h-1; y++ {
		row, up, down := gray[y*w:], gray[(y-1)*w:], gray[(y+1)*w:]
		for x := 1; x < w-1; x++ {
			lap := up[x] + down[x] + row[x-1] + row[x+1] - 4*row[x]
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

// grayscale renders img as a flat, row-major w*h luminance buffer
// using the standard 0.299/0.587/0.114 weighting over 0..65535 RGBA
// values. A single flat slice (rather than one []float64 per row)
// means one allocation instead of h, and the fast-path cases below
// read decoded pixel bytes directly instead of going through
// image.Image.At — a per-pixel interface call that both boxes a
// color.Color onto the heap and re-derives RGB through that type's
// general-purpose ColorModel conversion.
//
// *image.Gray and *image.RGBA are exact: their At/RGBA methods are
// simple enough that reading the underlying bytes directly reproduces
// the same values bit-for-bit (Gray has R=G=B=Y, and the fixed
// weights sum to 1; RGBA's bytes are already the premultiplied values
// RGBA() returns). *image.YCbCr (what the stdlib JPEG decoder
// produces, the dominant real-world format here) is a documented
// near-exact stand-in: Y is JPEG's own luma channel, mathematically
// equal to this same weighted RGB sum by construction of the YCbCr
// color space, modulo sub-ULP rounding and clipping on extreme,
// highly saturated colors — negligible for a heuristic score that's
// only ever compared relatively within one similarity group. Anything
// else (paletted, CMYK, NRGBA with partial alpha, ...) falls back to
// the general At() path, unchanged from before.
func grayscale(img image.Image, bounds image.Rectangle, w, h int) []float64 {
	gray := make([]float64, w*h)

	switch src := img.(type) {
	case *image.Gray:
		for y := 0; y < h; y++ {
			off := src.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			row := src.Pix[off : off+w]
			out := gray[y*w : y*w+w]
			for x, v := range row {
				out[x] = float64(v) * 257 // Y*0x101, matches color.Gray.RGBA()
			}
		}
	case *image.YCbCr:
		for y := 0; y < h; y++ {
			off := src.YOffset(bounds.Min.X, bounds.Min.Y+y)
			row := src.Y[off : off+w]
			out := gray[y*w : y*w+w]
			for x, v := range row {
				out[x] = float64(v) * 257
			}
		}
	case *image.RGBA:
		for y := 0; y < h; y++ {
			off := src.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			row := src.Pix[off : off+4*w]
			out := gray[y*w : y*w+w]
			for x := 0; x < w; x++ {
				i := x * 4
				r, g, b := float64(row[i]), float64(row[i+1]), float64(row[i+2])
				out[x] = (0.299*r + 0.587*g + 0.114*b) * 257
			}
		}
	default:
		for y := 0; y < h; y++ {
			out := gray[y*w : y*w+w]
			for x := 0; x < w; x++ {
				r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
				out[x] = 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
			}
		}
	}
	return gray
}
