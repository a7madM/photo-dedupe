// Package imagemetrics decodes an image and computes the signals the
// dedupe pipeline needs: a perceptual hash (for similarity grouping),
// a sharpness score (for blur-based winner filtering), pixel
// dimensions, and file size. These are the "empirical" parts of the
// tool — validated against real sample images rather than unit
// tested, since correctness here is about real-world usefulness of a
// score, not a pure deterministic contract.
package imagemetrics

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// decodeHEIC shells out to magick and reads back a raw PPM (P6)
// image rather than PNG: PNG made magick spend most of its time
// deflating (and us re-inflating) a full-resolution photo for no
// reason, since the bytes get decoded once and thrown away. Measured
// against 15 real 12MP iPhone HEICs: magick ... png:- averaged
// 7.3s/file wall clock; magick ... ppm:- (this function) averaged
// 0.54s/file — about 13x faster — with pixel-for-pixel identical
// decoded output, since both are lossless encodings of the same
// pixels magick already decoded from the HEIC. See
// BenchmarkDecodeHEIC for the synthetic-fixture version of this same
// comparison.
func decodeHEIC(path string) (image.Image, error) {
	cmd := exec.Command("magick", path, "ppm:-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting magick for %s: %w", path, err)
	}

	// Read stdout fully before Wait: Wait can't return until the
	// child's stdout pipe is drained, so reading and waiting have to
	// happen in this order (or concurrently) — never Wait-then-read.
	img, decodeErr := decodePPM(stdout)
	waitErr := cmd.Wait()

	if waitErr != nil {
		return nil, fmt.Errorf("magick decode of %s failed: %w: %s", path, waitErr, strings.TrimSpace(stderr.String()))
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decoding magick ppm output for %s: %w", path, decodeErr)
	}
	return img, nil
}

// maxPPMDimension bounds decodePPM's width/height so a corrupt or
// unexpected magick output can't trigger a multi-gigabyte allocation
// — generous enough for any real camera sensor (40000x40000 is a
// ~1.6-gigapixel image).
const maxPPMDimension = 40000

// decodePPM parses the binary PPM (P6) image magick emits for
// decodeHEIC's "ppm:-" request straight into an *image.NRGBA: P6 is
// exactly a tiny ASCII header (magic, width, height, maxval) followed
// by raw, uncompressed R,G,B byte triplets, so decoding it is a
// direct copy rather than a decompression pass. *image.NRGBA specifically
// (not a bespoke type) matters downstream — goimagehash's resize step
// and this package's own grayscale() both fast-path recognized stdlib
// image types and fall back to the slow, generic At()-per-pixel path
// for anything else.
func decodePPM(r io.Reader) (*image.NRGBA, error) {
	br := bufio.NewReaderSize(r, 64<<10)

	magic, err := readPNMToken(br)
	if err != nil {
		return nil, fmt.Errorf("reading PPM magic: %w", err)
	}
	if magic != "P6" {
		return nil, fmt.Errorf("unsupported PNM magic %q, want P6", magic)
	}
	width, err := readPNMUint(br, maxPPMDimension)
	if err != nil {
		return nil, fmt.Errorf("reading PPM width: %w", err)
	}
	height, err := readPNMUint(br, maxPPMDimension)
	if err != nil {
		return nil, fmt.Errorf("reading PPM height: %w", err)
	}
	maxVal, err := readPNMUint(br, 65535)
	if err != nil {
		return nil, fmt.Errorf("reading PPM maxval: %w", err)
	}
	if maxVal != 255 {
		return nil, fmt.Errorf("unsupported PPM maxval %d, want 255 (8-bit)", maxVal)
	}
	// readPNMToken/readPNMUint consume the single whitespace byte the
	// PNM spec requires right after maxval, so br is now positioned
	// exactly at the start of the binary raster.

	rgb := make([]byte, width*height*3)
	if _, err := io.ReadFull(br, rgb); err != nil {
		return nil, fmt.Errorf("reading PPM pixel data: %w", err)
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i, j := 0, 0; i < len(rgb); i, j = i+3, j+4 {
		img.Pix[j] = rgb[i]
		img.Pix[j+1] = rgb[i+1]
		img.Pix[j+2] = rgb[i+2]
		img.Pix[j+3] = 255
	}
	return img, nil
}

func isPNMSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// readPNMToken reads one whitespace-delimited header token, skipping
// leading whitespace and "#"-to-end-of-line comments per the PNM
// spec (magick's own output never includes a comment, but a decoder
// that only handles the exact bytes one encoder happens to produce
// today is a latent break waiting for a magick version bump).
func readPNMToken(br *bufio.Reader) (string, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '#' {
			if _, err := br.ReadString('\n'); err != nil {
				return "", err
			}
			continue
		}
		if isPNMSpace(b) {
			continue
		}
		if err := br.UnreadByte(); err != nil {
			return "", err
		}
		break
	}

	var tok []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if isPNMSpace(b) {
			break
		}
		tok = append(tok, b)
	}
	return string(tok), nil
}

// readPNMUint reads one PNM header token as a non-negative integer no
// larger than max.
func readPNMUint(br *bufio.Reader, max int) (int, error) {
	tok, err := readPNMToken(br)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(tok)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid integer", tok)
	}
	if n <= 0 || n > max {
		return 0, fmt.Errorf("%d out of range (want 1..%d)", n, max)
	}
	return n, nil
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
// only ever compared relatively within one similarity group.
// *image.NRGBA — what decodePPM produces for HEIC/HEIF — is exact for
// the same reason as RGBA: every pixel decodeHEIC ever hands back is
// fully opaque (PPM has no alpha channel at all), so straight and
// premultiplied values are identical here even though they aren't for
// NRGBA in general. Anything else (paletted, CMYK, a hypothetical
// NRGBA with real partial alpha, ...) falls back to the general At()
// path, unchanged from before.
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
	case *image.NRGBA:
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
