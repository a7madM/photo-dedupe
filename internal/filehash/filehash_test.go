package filehash

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}
	return p
}

func TestSHA256_SameContent_SameHash(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.jpg", "hello world")
	b := writeFile(t, dir, "b.jpg", "hello world")

	ha, err := SHA256(a)
	if err != nil {
		t.Fatalf("SHA256(a) error: %v", err)
	}
	hb, err := SHA256(b)
	if err != nil {
		t.Fatalf("SHA256(b) error: %v", err)
	}
	if ha != hb {
		t.Fatalf("hashes differ for identical content: %q vs %q", ha, hb)
	}
}

func TestSHA256_DifferentContent_DifferentHash(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.jpg", "hello world")
	b := writeFile(t, dir, "b.jpg", "goodbye world")

	ha, _ := SHA256(a)
	hb, _ := SHA256(b)
	if ha == hb {
		t.Fatalf("hashes match for different content: %q", ha)
	}
}

func TestSHA256_MissingFile_ReturnsError(t *testing.T) {
	_, err := SHA256(filepath.Join(t.TempDir(), "missing.jpg"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}
