package scene

import (
	"sort"
	"strings"

	"github.com/afternet/go-vibebot/internal/character"
)

// PreFilter ranks candidates by lexical overlap between the event text and
// each candidate's capability tags + display name, then returns either the
// top-K (when k > 0) or every candidate with a positive score.
//
// If no candidate shows any overlap, the original list is returned
// unchanged — pre-filter never silently excludes everyone.
//
// Ordering is stable on ties (original input order is preserved).
func PreFilter(eventText string, candidates []*character.Character, k int) []*character.Character {
	if len(candidates) == 0 {
		return candidates
	}
	eventTokens := tokenize(eventText)
	if len(eventTokens) == 0 {
		return candidates
	}

	type scored struct {
		c     *character.Character
		score int
		idx   int
	}
	ranked := make([]scored, len(candidates))
	totalScore := 0
	for i, c := range candidates {
		s := overlap(eventTokens, c)
		totalScore += s
		ranked[i] = scored{c: c, score: s, idx: i}
	}
	if totalScore == 0 {
		return candidates
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].idx < ranked[j].idx
	})

	out := make([]*character.Character, 0, len(ranked))
	for _, r := range ranked {
		if r.score == 0 {
			break
		}
		out = append(out, r.c)
		if k > 0 && len(out) >= k {
			break
		}
	}
	return out
}

// tokenize lowercases s and splits it on non-alphanumeric runs, dropping
// tokens shorter than 3 characters (low signal).
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 3 {
			out = append(out, cur.String())
		}
		cur.Reset()
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// overlap counts the number of event tokens that appear (as a substring
// either way) in the candidate's name or capability tags. Substring match
// makes simple stem variations ("sandwich" vs "sandwiches", "panic" vs
// "panicking") count without a real stemmer.
func overlap(eventTokens []string, c *character.Character) int {
	candTokens := tokenize(c.Name + " " + strings.Join(c.Capabilities, " "))
	if len(candTokens) == 0 {
		return 0
	}
	count := 0
	for _, et := range eventTokens {
		for _, ct := range candTokens {
			if strings.Contains(et, ct) || strings.Contains(ct, et) {
				count++
				break
			}
		}
	}
	return count
}
