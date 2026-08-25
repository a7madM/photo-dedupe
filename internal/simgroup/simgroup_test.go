package simgroup

import "testing"

func TestGroup_NoEdges_EachItemAlone(t *testing.T) {
	dist := func(i, j int) int { return 100 }

	got := Group(3, 10, dist)

	if len(got) != 3 {
		t.Fatalf("Group(...) = %v, want 3 singleton groups", got)
	}
}

func TestGroup_AllWithinThreshold_OneGroup(t *testing.T) {
	dist := func(i, j int) int { return 5 }

	got := Group(3, 10, dist)

	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("Group(...) = %v, want one group of 3", got)
	}
}

func TestGroup_TransitiveChain_MergedIntoOneGroup(t *testing.T) {
	// 0-1 close, 1-2 close, but 0-2 far: still one group via transitivity.
	dist := func(i, j int) int {
		pairs := map[[2]int]int{
			{0, 1}: 2,
			{1, 2}: 2,
			{0, 2}: 50,
		}
		key := [2]int{i, j}
		if i > j {
			key = [2]int{j, i}
		}
		return pairs[key]
	}

	got := Group(3, 10, dist)

	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("Group(...) = %v, want one merged group of 3", got)
	}
}

func TestGroup_TwoSeparateClusters(t *testing.T) {
	// {0,1} close to each other, {2,3} close to each other, clusters far apart.
	dist := func(i, j int) int {
		inFirst := func(x int) bool { return x == 0 || x == 1 }
		if inFirst(i) == inFirst(j) {
			return 1
		}
		return 100
	}

	got := Group(4, 10, dist)

	if len(got) != 2 {
		t.Fatalf("Group(...) = %v, want 2 groups", got)
	}
	for _, g := range got {
		if len(g) != 2 {
			t.Fatalf("group %v has size %d, want 2", g, len(g))
		}
	}
}

func TestGroup_ZeroItems_ReturnsEmpty(t *testing.T) {
	got := Group(0, 10, func(i, j int) int { return 0 })

	if len(got) != 0 {
		t.Fatalf("Group(0, ...) = %v, want empty", got)
	}
}
