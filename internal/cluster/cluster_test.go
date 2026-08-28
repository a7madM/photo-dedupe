package cluster

import (
	"testing"
	"time"
)

func TestGroup_EmptyInput_ReturnsEmpty(t *testing.T) {
	got := Group(nil, 0)

	if len(got) != 0 {
		t.Fatalf("Group(nil) = %v, want empty", got)
	}
}

func TestGroup_SingleItem_ReturnsOneGroupWithIt(t *testing.T) {
	ts := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	got := Group([]time.Time{ts}, time.Minute)

	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != 0 {
		t.Fatalf("Group([ts]) = %v, want [[0]]", got)
	}
}

func at(t *testing.T, hhmmss string) time.Time {
	t.Helper()
	tm, err := time.Parse("15:04:05", hhmmss)
	if err != nil {
		t.Fatalf("bad time literal %q: %v", hhmmss, err)
	}
	return tm
}

func TestGroup_TwoItemsWithinGap_OneGroup(t *testing.T) {
	a := at(t, "12:00:00")
	b := at(t, "12:00:30")

	got := Group([]time.Time{a, b}, time.Minute)

	if len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("Group(a,b within gap) = %v, want one group of 2", got)
	}
}

func TestGroup_TwoItemsBeyondGap_TwoGroups(t *testing.T) {
	a := at(t, "12:00:00")
	b := at(t, "12:05:00")

	got := Group([]time.Time{a, b}, time.Minute)

	if len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 1 {
		t.Fatalf("Group(a,b beyond gap) = %v, want two groups of 1", got)
	}
}

func TestGroup_UnsortedInput_SortsBeforeGrouping(t *testing.T) {
	c := at(t, "12:10:00") // index 0
	a := at(t, "12:00:00") // index 1
	b := at(t, "12:00:10") // index 2

	got := Group([]time.Time{c, a, b}, time.Minute)

	if len(got) != 2 {
		t.Fatalf("Group(unsorted) = %v, want 2 groups", got)
	}
	if len(got[0]) != 2 || got[0][0] != 1 || got[0][1] != 2 {
		t.Fatalf("first group = %v, want [1 2] (a then b, chronological)", got[0])
	}
	if len(got[1]) != 1 || got[1][0] != 0 {
		t.Fatalf("second group = %v, want [0] (c)", got[1])
	}
}

func TestGroup_MultipleGroups_EachSortedInternally(t *testing.T) {
	e := at(t, "14:00:15") // index 0
	d := at(t, "14:00:00") // index 1
	c := at(t, "09:00:40") // index 2
	b := at(t, "09:00:20") // index 3
	a := at(t, "09:00:00") // index 4

	got := Group([]time.Time{e, d, c, b, a}, time.Minute)

	want := [][]int{{4, 3, 2}, {1, 0}}
	if len(got) != len(want) {
		t.Fatalf("Group(...) = %v, want %v", got, want)
	}
	for gi := range want {
		if len(got[gi]) != len(want[gi]) {
			t.Fatalf("group %d = %v, want %v", gi, got[gi], want[gi])
		}
		for ii := range want[gi] {
			if got[gi][ii] != want[gi][ii] {
				t.Fatalf("group %d = %v, want %v", gi, got[gi], want[gi])
			}
		}
	}
}
