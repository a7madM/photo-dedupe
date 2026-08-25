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
	item := Item{Path: "a.jpg", Timestamp: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}

	got := Group([]Item{item}, time.Minute)

	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != item {
		t.Fatalf("Group([a]) = %v, want [[a]]", got)
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
	a := Item{Path: "a.jpg", Timestamp: at(t, "12:00:00")}
	b := Item{Path: "b.jpg", Timestamp: at(t, "12:00:30")}

	got := Group([]Item{a, b}, time.Minute)

	if len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("Group(a,b within gap) = %v, want one group of 2", got)
	}
}

func TestGroup_TwoItemsBeyondGap_TwoGroups(t *testing.T) {
	a := Item{Path: "a.jpg", Timestamp: at(t, "12:00:00")}
	b := Item{Path: "b.jpg", Timestamp: at(t, "12:05:00")}

	got := Group([]Item{a, b}, time.Minute)

	if len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 1 {
		t.Fatalf("Group(a,b beyond gap) = %v, want two groups of 1", got)
	}
}

func TestGroup_UnsortedInput_SortsBeforeGrouping(t *testing.T) {
	a := Item{Path: "a.jpg", Timestamp: at(t, "12:00:00")}
	b := Item{Path: "b.jpg", Timestamp: at(t, "12:00:10")}
	c := Item{Path: "c.jpg", Timestamp: at(t, "12:10:00")}

	got := Group([]Item{c, a, b}, time.Minute)

	if len(got) != 2 {
		t.Fatalf("Group(unsorted) = %v, want 2 groups", got)
	}
	if got[0][0] != a || got[0][1] != b {
		t.Fatalf("first group = %v, want [a b] in chronological order", got[0])
	}
	if got[1][0] != c {
		t.Fatalf("second group = %v, want [c]", got[1])
	}
}

func TestGroup_MultipleGroups_EachSortedInternally(t *testing.T) {
	a := Item{Path: "a.jpg", Timestamp: at(t, "09:00:00")}
	b := Item{Path: "b.jpg", Timestamp: at(t, "09:00:20")}
	c := Item{Path: "c.jpg", Timestamp: at(t, "09:00:40")}
	d := Item{Path: "d.jpg", Timestamp: at(t, "14:00:00")}
	e := Item{Path: "e.jpg", Timestamp: at(t, "14:00:15")}

	got := Group([]Item{e, d, c, b, a}, time.Minute)

	want := [][]Item{{a, b, c}, {d, e}}
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
