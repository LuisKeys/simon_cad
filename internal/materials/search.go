package materials

import (
	"sort"
	"strings"
	"unicode"
)

// Match is one search result with its relevance score.
type Match struct {
	Material Material `json:"material"`
	Score    int      `json:"score"`
}

func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

var synonyms = map[string][]string{
	"metallic": {"metal"}, "shiny": {"polished", "glossy"}, "silver": {"aluminum", "steel"},
	"aluminium": {"aluminum"}, "plastic": {"abs", "pla", "petg"}, "transparent": {"clear", "glass"},
	"see": {"clear"}, "through": {"clear"}, "circuit": {"pcb"}, "board": {"pcb"}, "carbon": {"carbon_fiber"},
}

// Search ranks materials against a free-text query using name, id, category and tags.
// Results are best-first; ties are broken by name so output is deterministic.
func (l *Library) Search(query string, limit int) []Match {
	q := tokens(query)
	var expanded []string
	for _, t := range q {
		expanded = append(expanded, t)
		expanded = append(expanded, synonyms[t]...)
	}
	var out []Match
	for _, m := range l.List("") {
		hay := map[string]int{}
		for _, t := range tokens(m.Name + " " + strings.ReplaceAll(m.ID, "_", " ")) {
			hay[t] = 3
		}
		for _, t := range tokens(m.Category) {
			hay[t] = max(hay[t], 2)
		}
		for _, tag := range m.Tags {
			for _, t := range tokens(tag) {
				hay[t] = max(hay[t], 2)
			}
		}
		score := 0
		for _, t := range expanded {
			if w, ok := hay[t]; ok {
				score += w
				continue
			}
			for h, w := range hay { // prefix match: "alum" -> "aluminum"
				if len(t) >= 3 && strings.HasPrefix(h, t) {
					score += w - 1
					break
				}
			}
		}
		if score > 0 {
			out = append(out, Match{m, score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Material.Name < out[j].Material.Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Ambiguous reports whether the top results are too close for the agent to pick one:
// it should then offer the options instead of guessing.
func Ambiguous(ms []Match) bool {
	if len(ms) < 2 {
		return false
	}
	return ms[1].Score*10 >= ms[0].Score*8 // runner-up within 80% of the best
}
