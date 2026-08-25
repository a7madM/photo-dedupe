// Package pick decides the winner among a group of already-scored,
// similarity-matched image candidates: sharpness first (as an
// eligibility filter, not a hard ranking), then resolution, then
// file size as a final tiebreak.
package pick

// Candidate is one image within a similarity group, already scored.
type Candidate struct {
	Path      string
	Sharpness float64
	Width     int
	Height    int
	SizeBytes int64
}

func (c Candidate) resolution() int64 {
	return int64(c.Width) * int64(c.Height)
}

// Pick selects the winner from candidates and returns the rest as
// losers, in their original relative order.
//
// A candidate is eligible to win only if its Sharpness is within
// blurThreshold of the group's maximum Sharpness (the sharpest
// candidate is always eligible). Among eligible candidates, the
// highest resolution wins; ties break on largest SizeBytes; a
// remaining tie breaks on lexicographically smallest Path, for
// determinism.
func Pick(candidates []Candidate, blurThreshold float64) (winner Candidate, losers []Candidate) {
	if len(candidates) == 0 {
		return Candidate{}, nil
	}

	maxSharpness := candidates[0].Sharpness
	for _, c := range candidates[1:] {
		if c.Sharpness > maxSharpness {
			maxSharpness = c.Sharpness
		}
	}

	winnerIdx := -1
	for i, c := range candidates {
		if maxSharpness-c.Sharpness > blurThreshold {
			continue
		}
		if winnerIdx == -1 || better(c, candidates[winnerIdx]) {
			winnerIdx = i
		}
	}

	winner = candidates[winnerIdx]
	losers = make([]Candidate, 0, len(candidates)-1)
	for i, c := range candidates {
		if i != winnerIdx {
			losers = append(losers, c)
		}
	}
	return winner, losers
}

// better reports whether a should be preferred over the current best b.
func better(a, b Candidate) bool {
	if a.resolution() != b.resolution() {
		return a.resolution() > b.resolution()
	}
	if a.SizeBytes != b.SizeBytes {
		return a.SizeBytes > b.SizeBytes
	}
	return a.Path < b.Path
}
