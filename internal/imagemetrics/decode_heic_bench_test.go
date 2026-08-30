package imagemetrics

import (
	"path/filepath"
	"testing"
)

// benchHEICSize keeps the synthetic fixture in the same ballpark as a
// real phone photo (12MP) so the PNG-vs-PPM gap these benchmarks
// measure is representative of real scan times, not just a
// microbenchmark artifact.
const benchHEICSize = 3464 // 3464x3464 ≈ 12MP

func benchHEICFixture(b *testing.B) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "bench.heic")
	heicFixture(b, path, checkerboard(benchHEICSize))
	return path
}

// BenchmarkDecodeHEIC measures the current implementation: magick
// ... ppm:- piped straight into decodePPM. Compare against
// BenchmarkDecodeHEIC_OldPNGPath (magick ... png:- piped into
// image.Decode, kept in decode_heic_test.go as the pre-optimization
// reference) with:
//
//	go test ./internal/imagemetrics/ -run '^$' -bench DecodeHEIC -benchmem -count=6
//
// The synthetic checkerboard fixture always shows ppm ahead, but by a
// smaller margin than real photos — a checkerboard is adversarial for
// HEIC's own block-based decode (lots of high-frequency detail),
// which dilutes the PNG-encode saving this optimization actually
// targets. The representative number is the one in decodeHEIC's doc
// comment, measured against real iPhone photos.
func BenchmarkDecodeHEIC(b *testing.B) {
	path := benchHEICFixture(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := decodeHEIC(path); err != nil {
			b.Fatalf("decodeHEIC: %v", err)
		}
	}
}

func BenchmarkDecodeHEIC_OldPNGPath(b *testing.B) {
	path := benchHEICFixture(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := decodeHEICViaPNG(path); err != nil {
			b.Fatalf("decodeHEICViaPNG: %v", err)
		}
	}
}
