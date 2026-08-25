// Package simgroup partitions a set of items into similarity clusters
// using union-find: item i and item j merge into the same cluster
// whenever dist(i, j) <= threshold. This is the connected-components
// logic underlying similarity grouping; the distance metric itself
// (e.g. perceptual hash distance) is supplied by the caller.
package simgroup

// DistanceFunc returns a distance between items i and j (0..n-1).
// Implementations are expected to be symmetric: dist(i,j) == dist(j,i).
type DistanceFunc func(i, j int) int

// Group partitions n items (indices 0..n-1) into clusters where an
// edge exists between i and j whenever dist(i, j) <= threshold.
// Clusters are returned as slices of indices; order is not specified.
func Group(n int, threshold int, dist DistanceFunc) [][]int {
	if n == 0 {
		return nil
	}

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}

	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if dist(i, j) <= threshold {
				union(i, j)
			}
		}
	}

	groups := make(map[int][]int)
	for i := 0; i < n; i++ {
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	result := make([][]int, 0, len(groups))
	for _, g := range groups {
		result = append(result, g)
	}
	return result
}
