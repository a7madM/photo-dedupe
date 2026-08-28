package webui

import "testing"

func TestImageCache_MissThenHit(t *testing.T) {
	c := newImageCache(2)

	if _, ok := c.get("a"); ok {
		t.Fatal("get on empty cache returned a hit")
	}

	c.put("a", []byte("A"))
	data, ok := c.get("a")
	if !ok || string(data) != "A" {
		t.Fatalf("get(a) = %q, %v; want \"A\", true", data, ok)
	}
}

func TestImageCache_EvictsLeastRecentlyUsed(t *testing.T) {
	c := newImageCache(2)

	c.put("a", []byte("A"))
	c.put("b", []byte("B"))
	c.put("c", []byte("C")) // capacity 2: evicts "a", the least recently touched

	if _, ok := c.get("a"); ok {
		t.Fatal("a should have been evicted")
	}
	if _, ok := c.get("b"); !ok {
		t.Fatal("b should still be cached")
	}
	if _, ok := c.get("c"); !ok {
		t.Fatal("c should still be cached")
	}
}

func TestImageCache_GetRefreshesRecency(t *testing.T) {
	c := newImageCache(2)

	c.put("a", []byte("A"))
	c.put("b", []byte("B"))
	c.get("a")               // touch "a" so "b" becomes the least recently used
	c.put("c", []byte("C")) // evicts "b", not "a"

	if _, ok := c.get("b"); ok {
		t.Fatal("b should have been evicted after a was refreshed")
	}
	if _, ok := c.get("a"); !ok {
		t.Fatal("a should still be cached after being refreshed")
	}
}

func TestImageCache_PutSameKeyUpdatesValueWithoutGrowing(t *testing.T) {
	c := newImageCache(2)

	c.put("a", []byte("A1"))
	c.put("a", []byte("A2"))

	data, ok := c.get("a")
	if !ok || string(data) != "A2" {
		t.Fatalf("get(a) = %q, %v; want \"A2\", true", data, ok)
	}
	if c.ll.Len() != 1 {
		t.Fatalf("cache has %d entries, want 1 (re-put of an existing key must not grow it)", c.ll.Len())
	}
}
