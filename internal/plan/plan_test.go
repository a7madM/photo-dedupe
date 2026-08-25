package plan

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestWriteRead_RoundTrip_PreservesData(t *testing.T) {
	original := Plan{
		Version:     1,
		Root:        "/Volumes/USB/photos",
		GapSeconds:  60,
		GeneratedAt: time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
		Groups: []Group{
			{
				ID: 1,
				Winner: FileRecord{
					Path:        "/Volumes/USB/photos/IMG_001.jpg",
					ContentHash: "abc123",
					Width:       4032,
					Height:      3024,
					Sharpness:   120.5,
					SizeBytes:   5_242_880,
				},
				Losers: []FileRecord{
					{
						Path:        "/Volumes/USB/photos/IMG_002.jpg",
						ContentHash: "def456",
						Width:       4032,
						Height:      3024,
						Sharpness:   80.1,
						SizeBytes:   5_100_000,
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, original); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	got, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	if got.Version != original.Version ||
		got.Root != original.Root ||
		got.GapSeconds != original.GapSeconds ||
		!got.GeneratedAt.Equal(original.GeneratedAt) {
		t.Fatalf("round-tripped plan header = %+v, want %+v", got, original)
	}
	if len(got.Groups) != 1 || got.Groups[0].Winner != original.Groups[0].Winner {
		t.Fatalf("round-tripped winner = %+v, want %+v", got.Groups, original.Groups)
	}
	if len(got.Groups[0].Losers) != 1 || got.Groups[0].Losers[0] != original.Groups[0].Losers[0] {
		t.Fatalf("round-tripped losers = %+v, want %+v", got.Groups[0].Losers, original.Groups[0].Losers)
	}
}

func TestWrite_UsesStableFieldNames(t *testing.T) {
	p := Plan{
		Version:     1,
		Root:        "/x",
		GapSeconds:  60,
		GeneratedAt: time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
		Groups: []Group{
			{
				ID:     1,
				Winner: FileRecord{Path: "a.jpg", ContentHash: "h1", Width: 100, Height: 200, Sharpness: 1.5, SizeBytes: 10},
				Losers: []FileRecord{{Path: "b.jpg", ContentHash: "h2", Width: 100, Height: 200, Sharpness: 0.5, SizeBytes: 9}},
			},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, p); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	for _, key := range []string{"version", "root", "gap_seconds", "generated_at", "groups"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("top-level JSON missing key %q, got keys %v", key, raw)
		}
	}

	groups, _ := raw["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups = %v, want 1 entry", raw["groups"])
	}
	group, _ := groups[0].(map[string]any)
	for _, key := range []string{"id", "winner", "losers"} {
		if _, ok := group[key]; !ok {
			t.Errorf("group JSON missing key %q, got keys %v", key, group)
		}
	}

	winner, _ := group["winner"].(map[string]any)
	for _, key := range []string{"path", "content_hash", "width", "height", "sharpness", "size_bytes"} {
		if _, ok := winner[key]; !ok {
			t.Errorf("winner JSON missing key %q, got keys %v", key, winner)
		}
	}
}
