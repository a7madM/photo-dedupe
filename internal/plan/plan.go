// Package plan defines the on-disk schema for a dedupe scan's output
// (.dedupe-plan.json): the groups it found, the winner and losers in
// each, and the data apply/restore need to act on it later.
package plan

import (
	"encoding/json"
	"io"
	"time"
)

// FileRecord describes one image within a group, including the
// content hash used by apply to detect drift since the scan ran.
type FileRecord struct {
	Path        string  `json:"path"`
	ContentHash string  `json:"content_hash"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Sharpness   float64 `json:"sharpness"`
	SizeBytes   int64   `json:"size_bytes"`
}

// Group is one time-clustered, similarity-filtered set of images:
// a chosen winner and the losers to be quarantined.
type Group struct {
	ID     int          `json:"id"`
	Winner FileRecord   `json:"winner"`
	Losers []FileRecord `json:"losers"`
}

// Plan is the full output of a scan.
type Plan struct {
	Version     int       `json:"version"`
	Root        string    `json:"root"`
	GapSeconds  int       `json:"gap_seconds"`
	GeneratedAt time.Time `json:"generated_at"`
	Groups      []Group   `json:"groups"`
}

// Write serializes p as indented JSON to w.
func Write(w io.Writer, p Plan) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

// Read deserializes a Plan previously written by Write.
func Read(r io.Reader) (Plan, error) {
	var p Plan
	err := json.NewDecoder(r).Decode(&p)
	return p, err
}
