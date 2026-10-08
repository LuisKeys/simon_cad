package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/accelone/simoncad/internal/config"
)

// Cost is the estimated spend of a Usage. Estimation is opt-in: a cloud model with no
// configured price contributes no amount and is listed in Unpriced instead, so the
// application reports tokens only rather than an invented number. Local models are never
// spend: their tokens are counted in LocalTokens and contribute no amount.
type Cost struct {
	Amount   float64  `json:"amount"`
	Currency string   `json:"currency"`
	Known    bool     `json:"known"`              // at least one model was priced
	Partial  bool     `json:"partial,omitempty"`  // some cloud models were priced and some were not
	Unpriced []string `json:"unpriced,omitempty"` // cloud models with no configured price
	// Amounts is the spend per currency when the priced models use more than one; Amount and
	// Currency are then the largest of them.
	Amounts map[string]float64 `json:"amounts,omitempty"`
	// LocalTokens and CloudTokens split the tokens by where the model ran.
	LocalTokens int `json:"local_tokens"`
	CloudTokens int `json:"cloud_tokens"`
}

// Estimate costs a Usage with the prices configured for each model that served it. Usage
// that no provider attributed to a model (recorded sessions, test doubles) cannot be costed.
func Estimate(u Usage) Cost {
	c := Cost{Currency: config.Currency()}
	amounts := map[string]float64{}
	for _, m := range u.Models() {
		if config.Local(m.Provider) {
			c.LocalTokens += m.InputTokens + m.OutputTokens
			continue
		}
		c.CloudTokens += m.InputTokens + m.OutputTokens
		p, cur, ok := config.PriceForModel(m.Provider, m.Model)
		if !ok {
			c.Unpriced = append(c.Unpriced, m.Model)
			continue
		}
		amounts[cur] += p.Cost(m.InputTokens, m.CachedInputTokens, m.OutputTokens)
		c.Known = true
	}
	for cur, a := range amounts {
		if c.Amount == 0 || a > c.Amount || (a == c.Amount && cur < c.Currency) {
			c.Amount, c.Currency = a, cur
		}
	}
	if len(amounts) > 1 {
		c.Amounts = amounts
	}
	c.Partial = c.Known && len(c.Unpriced) > 0
	return c
}

// String renders an amount with enough decimals to be meaningful at agent-turn scale.
func (c Cost) String() string {
	if !c.Known {
		return ""
	}
	s := fmt.Sprintf("%s %.4f", c.Currency, c.Amount)
	if len(c.Amounts) > 1 {
		curs := make([]string, 0, len(c.Amounts))
		for cur := range c.Amounts {
			curs = append(curs, cur)
		}
		sort.Strings(curs)
		parts := make([]string, len(curs))
		for i, cur := range curs {
			parts[i] = fmt.Sprintf("%s %.4f", cur, c.Amounts[cur])
		}
		s = strings.Join(parts, " + ")
	}
	if c.Partial {
		s += " (partial: no price for " + joinShort(c.Unpriced) + ")"
	}
	return s
}

// Report is a one-line human summary of a Usage and its cost. Only cloud models are spend;
// local tokens are reported as such.
func Report(u Usage) string {
	s := fmt.Sprintf("%d tokens (%d in / %d out)", u.Tokens(), u.InputTokens, u.OutputTokens)
	if u.CachedInputTokens > 0 {
		s += fmt.Sprintf(", %d of the input cached", u.CachedInputTokens)
	}
	c := Estimate(u)
	switch {
	case c.LocalTokens > 0 && c.CloudTokens == 0:
		return s + " — local model, no cost"
	case c.Known && c.LocalTokens > 0:
		return s + fmt.Sprintf(" — est. %s for %d cloud tokens; %d local tokens, no cost", c.String(), c.CloudTokens, c.LocalTokens)
	case c.Known:
		return s + " — est. " + c.String()
	case c.LocalTokens > 0:
		return s + fmt.Sprintf(" — %d local tokens (no cost); no prices configured for the %d cloud tokens", c.LocalTokens, c.CloudTokens)
	}
	return s + " — no prices configured, tokens only"
}

func joinShort(items []string) string {
	const max = 3
	out := ""
	for i, it := range items {
		if i == max {
			return out + fmt.Sprintf(", +%d more", len(items)-max)
		}
		if i > 0 {
			out += ", "
		}
		out += it
	}
	return out
}
