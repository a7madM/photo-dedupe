package pick

import "testing"

func TestPick_SingleCandidate_IsWinnerNoLosers(t *testing.T) {
	c := Candidate{Path: "a.jpg", Sharpness: 100, Width: 4000, Height: 3000, SizeBytes: 5_000_000}

	winner, losers := Pick([]Candidate{c}, 20)

	if winner != c {
		t.Fatalf("winner = %+v, want %+v", winner, c)
	}
	if len(losers) != 0 {
		t.Fatalf("losers = %+v, want none", losers)
	}
}

func TestPick_BlurryCandidateExcluded_SharperLowerResWins(t *testing.T) {
	sharpButSmall := Candidate{Path: "sharp.jpg", Sharpness: 100, Width: 1000, Height: 1000, SizeBytes: 1}
	blurryButBig := Candidate{Path: "blurry.jpg", Sharpness: 10, Width: 9000, Height: 9000, SizeBytes: 1}

	winner, losers := Pick([]Candidate{sharpButSmall, blurryButBig}, 20)

	if winner != sharpButSmall {
		t.Fatalf("winner = %+v, want the sharp one despite lower resolution", winner)
	}
	if len(losers) != 1 || losers[0] != blurryButBig {
		t.Fatalf("losers = %+v, want [blurryButBig]", losers)
	}
}

func TestPick_WithinBlurThreshold_HighestResolutionWins(t *testing.T) {
	a := Candidate{Path: "a.jpg", Sharpness: 100, Width: 1000, Height: 1000, SizeBytes: 1}
	b := Candidate{Path: "b.jpg", Sharpness: 90, Width: 4000, Height: 3000, SizeBytes: 1}

	winner, losers := Pick([]Candidate{a, b}, 20)

	if winner != b {
		t.Fatalf("winner = %+v, want b (both eligible, b has higher resolution)", winner)
	}
	if len(losers) != 1 || losers[0] != a {
		t.Fatalf("losers = %+v, want [a]", losers)
	}
}

func TestPick_SameResolution_LargestSizeWins(t *testing.T) {
	a := Candidate{Path: "a.jpg", Sharpness: 100, Width: 4000, Height: 3000, SizeBytes: 5_000_000}
	b := Candidate{Path: "b.jpg", Sharpness: 100, Width: 4000, Height: 3000, SizeBytes: 8_000_000}

	winner, _ := Pick([]Candidate{a, b}, 20)

	if winner != b {
		t.Fatalf("winner = %+v, want b (larger file size as tiebreak)", winner)
	}
}

func TestPick_CompleteTie_LexicographicallySmallestPathWins(t *testing.T) {
	a := Candidate{Path: "a.jpg", Sharpness: 100, Width: 4000, Height: 3000, SizeBytes: 5_000_000}
	b := Candidate{Path: "b.jpg", Sharpness: 100, Width: 4000, Height: 3000, SizeBytes: 5_000_000}

	winner, _ := Pick([]Candidate{b, a}, 20)

	if winner != a {
		t.Fatalf("winner = %+v, want a (deterministic path tiebreak)", winner)
	}
}

func TestPick_EmptyInput_ReturnsZeroValueNoPanic(t *testing.T) {
	winner, losers := Pick(nil, 20)

	if winner != (Candidate{}) {
		t.Fatalf("winner = %+v, want zero value", winner)
	}
	if len(losers) != 0 {
		t.Fatalf("losers = %+v, want none", losers)
	}
}
