package apply

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/a7madM/photo-dedupe/internal/filehash"
	"github.com/a7madM/photo-dedupe/internal/plan"
)

// benchPlan writes n files of size bytes under a fresh temp root and
// returns a plan.Plan whose winners reference them with correct
// recorded hashes, so Apply takes its normal all-hashes-verify path
// end to end (moves included) rather than short-circuiting on drift.
func benchPlan(b *testing.B, n, size int) plan.Plan {
	b.Helper()
	root := b.TempDir()

	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		b.Fatalf("generating fixture bytes: %v", err)
	}

	groups := make([]plan.Group, n)
	for i := 0; i < n; i++ {
		path := filepath.Join(root, "photo"+itoa(i)+".jpg")
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			b.Fatalf("writing fixture: %v", err)
		}
		hash, err := filehash.SHA256(path)
		if err != nil {
			b.Fatalf("hashing fixture: %v", err)
		}
		groups[i] = plan.Group{
			ID:     i,
			Winner: plan.FileRecord{Path: path, ContentHash: hash, SizeBytes: int64(size)},
		}
	}
	return plan.Plan{Root: root, Groups: groups}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// benchApply times Apply over a fresh benchPlan each iteration — the
// plan's files get moved out from under it, so setup must be excluded
// from the timer and redone every round.
func benchApply(b *testing.B, n, size int) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		p := benchPlan(b, n, size)
		b.StartTimer()

		if _, err := Apply(p); err != nil {
			b.Fatalf("Apply: %v", err)
		}
	}
}

// 200 files at 3MB mirrors a burst-heavy photo library subset (plenty
// of near-duplicates queued for quarantine) with realistic per-file
// hashing cost.
func BenchmarkApply_200Files_3MB(b *testing.B) {
	benchApply(b, 200, 3<<20)
}

// BenchmarkApply_200Files_3MB_SingleCore pins GOMAXPROCS to 1 so
// checkAll's worker pool still spawns NumCPU() goroutines but only
// one ever actually runs at a time — an approximation of the fully
// sequential hashing this package used before, for a like-for-like
// comparison against the benchmark above without keeping two copies
// of the hashing logic around.
func BenchmarkApply_200Files_3MB_SingleCore(b *testing.B) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	benchApply(b, 200, 3<<20)
}
