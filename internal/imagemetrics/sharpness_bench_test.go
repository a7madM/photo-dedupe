package imagemetrics

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"
)

// benchGray/benchYCbCr/benchRGBA build a 4032x3024 (12MP, a common
// phone-camera resolution) synthetic image directly in each concrete
// type sharpness's fast paths dispatch on, so the benchmark measures
// the pixel-access strategy itself rather than decode cost.

func benchGray(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 2749 % 256)
	}
	return img
}

// benchYCbCr round-trips a real RGB image through the standard JPEG
// encoder/decoder rather than hand-assembling Y/Cb/Cr planes:
// independently-random chroma planes can't occur from an actual
// encode (valid RGB always round-trips through YCbCr without
// clipping), so synthesizing them directly would exercise a case a
// real decoded photo never produces.
func benchYCbCr(w, h int) *image.YCbCr {
	src := benchRGBA(w, h)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	img, err := jpeg.Decode(&buf)
	if err != nil {
		panic(err)
	}
	return img.(*image.YCbCr)
}

func benchRGBA(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x * 7) % 256), G: uint8((y * 13) % 256), B: uint8((x + y) % 256), A: 255,
			})
		}
	}
	return img
}

// genericWrapper hides the concrete type behind a plain image.Image,
// forcing sharpness through the At()-based fallback path — this is
// what every pixel access cost before this optimization.
type genericWrapper struct{ image.Image }

const benchW, benchH = 4032, 3024

func BenchmarkSharpness_Gray(b *testing.B) {
	img := benchGray(benchW, benchH)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sharpness(img)
	}
}

func BenchmarkSharpness_Gray_Fallback(b *testing.B) {
	img := genericWrapper{benchGray(benchW, benchH)}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sharpness(img)
	}
}

func BenchmarkSharpness_YCbCr(b *testing.B) {
	img := benchYCbCr(benchW, benchH)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sharpness(img)
	}
}

func BenchmarkSharpness_YCbCr_Fallback(b *testing.B) {
	img := genericWrapper{benchYCbCr(benchW, benchH)}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sharpness(img)
	}
}

func BenchmarkSharpness_RGBA(b *testing.B) {
	img := benchRGBA(benchW, benchH)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sharpness(img)
	}
}

func BenchmarkSharpness_RGBA_Fallback(b *testing.B) {
	img := genericWrapper{benchRGBA(benchW, benchH)}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sharpness(img)
	}
}

// TestSharpness_FastPathMatchesFallback pins down the correctness
// claim the fast paths depend on: each fast-path type must score
// within a tight tolerance of the generic At()-based path on the same
// pixels (exactly for Gray/RGBA, near-exactly for YCbCr — see the
// doc comment on grayscale). A smaller image keeps this test fast;
// the benchmarks above cover realistic resolutions.
func TestSharpness_FastPathMatchesFallback(t *testing.T) {
	const w, h = 97, 61 // deliberately not a round/aligned size

	cases := []struct {
		name   string
		img    image.Image
		relTol float64
	}{
		{"Gray", benchGray(w, h), 1e-9},
		{"RGBA", benchRGBA(w, h), 1e-9},
		{"YCbCr", benchYCbCr(w, h), 0.01},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fast := sharpness(c.img)
			slow := sharpness(genericWrapper{c.img})
			if slow == 0 {
				t.Fatalf("fallback sharpness = 0, test image not varied enough")
			}
			relErr := math.Abs(fast-slow) / slow
			if relErr > c.relTol {
				t.Fatalf("fast path = %v, fallback = %v, relative error %v exceeds tolerance %v", fast, slow, relErr, c.relTol)
			}
		})
	}
}
