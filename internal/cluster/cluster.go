// Package cluster groups timestamps into clusters using a gap-based
// rule: chronologically consecutive timestamps that are within a
// configurable gap of each other belong to the same cluster.
package cluster

import (
	"sort"
	"time"
)

// Group partitions timestamps (indices 0..len(timestamps)-1) into
// clusters, splitting wherever the gap between chronologically
// consecutive timestamps exceeds gap. Groups are returned in
// chronological order; each group's indices are ordered ascending by
// timestamp.
func Group(timestamps []time.Time, gap time.Duration) [][]int {
	if len(timestamps) == 0 {
		return nil
	}

	order := make([]int, len(timestamps))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		return timestamps[order[i]].Before(timestamps[order[j]])
	})

	groups := [][]int{{order[0]}}
	for _, idx := range order[1:] {
		last := groups[len(groups)-1]
		prev := last[len(last)-1]
		if timestamps[idx].Sub(timestamps[prev]) > gap {
			groups = append(groups, []int{idx})
		} else {
			groups[len(groups)-1] = append(last, idx)
		}
	}

	return groups
}
