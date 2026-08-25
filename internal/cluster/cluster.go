// Package cluster groups timestamped items into clusters using a
// gap-based rule: consecutive items (sorted by time) that are within
// a configurable gap of each other belong to the same cluster.
package cluster

import (
	"sort"
	"time"
)

// Item is a single timestamped unit to be clustered.
type Item struct {
	Path      string
	Timestamp time.Time
}

// Group sorts items by Timestamp ascending, then splits them into
// clusters wherever the gap between consecutive timestamps exceeds gap.
// Groups are returned in chronological order; each group's items are
// sorted ascending by Timestamp.
func Group(items []Item, gap time.Duration) [][]Item {
	if len(items) == 0 {
		return nil
	}

	sorted := make([]Item, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	groups := [][]Item{{sorted[0]}}
	for _, item := range sorted[1:] {
		last := groups[len(groups)-1]
		prev := last[len(last)-1]
		if item.Timestamp.Sub(prev.Timestamp) > gap {
			groups = append(groups, []Item{item})
		} else {
			groups[len(groups)-1] = append(last, item)
		}
	}

	return groups
}
