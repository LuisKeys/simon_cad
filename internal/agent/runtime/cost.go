package runtime

import (
	"fmt"

	"github.com/accelone/simoncad/internal/config"
)

// Cost is the estimated spend of a Usage. Estimation is opt-in: a model whose provider has
// no configured price contributes no amount and is listed in Unpriced instead, so the
// application reports tokens only rather than an invented number.
type Cost struct {
	Amount   float64  `json:"amount"`
	Currency string   `json:"currency"`
	Known    bool     `json:"known"`              // at least one model was priced
	Partial  bool     `json:"partial,omitempty"`  // some models were priced and some were not
	Unpriced []string `json:"unpriced,omitempty"` // models with no configured price
}

// Estimate costs a Usage with the prices configured per provider. Usage that no provider
// attributed to a model (recorded sessions, test doubles) cannot be costed.
func Estimate(u Usage) Cost {
	c := Cost{Currency: config.Currency()}
	for _, m := range u.Models() {
		p, ok := config.PriceFor(m.Provider)
		if !ok {
			c.Unpriced = append(c.Unpriced, m.Model)
			continue
		}
		c.Amount += p.Cost(m.InputTokens, m.CachedInputTokens, m.OutputTokens)
		c.Known = true
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
	if c.Partial {
		s += " (partial: no price for " + joinShort(c.Unpriced) + ")"
	}
	return s
}

// Report is a one-line human summary of a Usage and its cost.
func Report(u Usage) string {
	s := fmt.Sprintf("%d tokens (%d in / %d out)", u.Tokens(), u.InputTokens, u.OutputTokens)
	if u.CachedInputTokens > 0 {
		s += fmt.Sprintf(", %d of the input cached", u.CachedInputTokens)
	}
	if c := Estimate(u); c.Known {
		return s + " — est. " + c.String()
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
