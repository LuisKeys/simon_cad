package runtime

import (
	"regexp"
	"strings"

	"github.com/accelone/simoncad/internal/config"
)

// effortOrder ranks the configurable reasoning levels from least to most thinking. Which of
// them a model accepts varies per model and changes over time, so this provider never
// hard-codes a model's list: it sends what was asked for and adapts to the list the API
// names when it refuses.
var effortOrder = config.ReasoningEfforts

// validEffort reports whether s is a reasoning effort this application knows how to send.
func validEffort(s string) bool { return s != "" && config.ValidReasoningEffort(s) }

var (
	// "Unsupported value: 'none' is not supported with the 'gpt-x' model."
	unsupportedEffortRe = regexp.MustCompile(`(?i)unsupported value: '([a-z]+)' is not supported`)
	// "Supported values are: 'low', 'medium', 'high', 'xhigh', and 'max'."
	supportedListRe = regexp.MustCompile(`(?i)supported values are:([^.]*)`)
	quotedRe        = regexp.MustCompile(`'([a-z]+)'`)
)

// supportedEfforts reads the efforts an API error lists as acceptable (nil when it lists none).
func supportedEfforts(msg string) []string {
	m := supportedListRe.FindStringSubmatch(msg)
	if m == nil {
		return nil
	}
	var out []string
	for _, q := range quotedRe.FindAllStringSubmatch(strings.ToLower(m[1]), -1) {
		if validEffort(q[1]) {
			out = append(out, q[1])
		}
	}
	return out
}

// effortRank orders an effort; unknown values sort as "medium" so they land mid-scale.
func effortRank(e string) int {
	for i, x := range effortOrder {
		if x == e {
			return i
		}
	}
	return 3
}

// nearestEffort picks the supported effort closest to want, breaking ties towards less
// thinking: asking for less and getting more costs the user tokens they did not ask for.
func nearestEffort(want string, supported []string) (string, bool) {
	best, bestDist := "", 0
	for _, s := range supported {
		d := effortRank(s) - effortRank(want)
		if d < 0 {
			d = -d
		}
		if best == "" || d < bestDist || (d == bestDist && effortRank(s) < effortRank(best)) {
			best, bestDist = s, d
		}
	}
	return best, best != ""
}

// altEffort reads a provider error that rejected the reasoning effort sent and returns the
// supported value to retry with. It returns false when the error is about something else, or
// when the API refused a value this request did not send.
func altEffort(errMsg, sent string) (string, bool) {
	m := unsupportedEffortRe.FindStringSubmatch(errMsg)
	if m == nil || !strings.EqualFold(m[1], sent) {
		return "", false
	}
	alt, ok := nearestEffort(sent, supportedEfforts(errMsg))
	if !ok || strings.EqualFold(alt, sent) {
		return "", false
	}
	return alt, true
}
