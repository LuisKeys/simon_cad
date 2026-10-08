package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Price is what a model costs per million tokens, expressed in Currency(). A zero field
// means "not configured": the application then reports tokens only, never a guessed cost.
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	// CachedInput is the reduced rate providers charge for input served from their prompt
	// cache. Zero means "not configured", and cached input is then charged at Input: no
	// discount is ever assumed, because guessing one would understate the real spend.
	CachedInput float64 `json:"cached_input"`
}

// Set reports whether a price was configured at all.
func (p Price) Set() bool { return p.Input > 0 || p.Output > 0 || p.CachedInput > 0 }

// Cost is what one call's tokens cost at this price. cachedIn is the part of in that the
// provider served from its cache; it is charged at CachedInput when that rate is known.
func (p Price) Cost(in, cachedIn, out int) float64 {
	if cachedIn > in {
		cachedIn = in
	}
	cachedRate := p.CachedInput
	if cachedRate == 0 {
		cachedRate = p.Input
	}
	fresh := float64(in-cachedIn) * p.Input
	return (fresh + float64(cachedIn)*cachedRate + float64(out)*p.Output) / 1e6
}

// DefaultCurrency labels prices when SIMONCAD_CURRENCY is not set.
const DefaultCurrency = "USD"

const keyCurrency = "SIMONCAD_CURRENCY"

// Price env variable names per provider (per million tokens). The Ollama names are only
// listed so older settings files parse; local models are never priced.
var priceKeys = map[string]struct{ in, out, cached string }{
	ProviderOpenAI: {"OPENAI_INPUT_PRICE", "OPENAI_OUTPUT_PRICE", "OPENAI_CACHED_INPUT_PRICE"},
	ProviderClaude: {"ANTHROPIC_INPUT_PRICE", "ANTHROPIC_OUTPUT_PRICE", "ANTHROPIC_CACHED_INPUT_PRICE"},
	ProviderOllama: {"OLLAMA_INPUT_PRICE", "OLLAMA_OUTPUT_PRICE", "OLLAMA_CACHED_INPUT_PRICE"},
}

// Currency is the label prices are given in (default USD).
func Currency() string {
	return strings.ToUpper(GetOr(keyCurrency, DefaultCurrency))
}

// PriceFor returns the configured price of a provider's primary-slot model. The second
// result is false when no price is configured, which is what tells the caller to report
// tokens only, and always for local models, which cost nothing per token.
func PriceFor(provider string) (Price, bool) {
	if _, ok := priceKeys[provider]; !ok || Local(provider) {
		return Price{}, false
	}
	p := ModelConfigFor(SlotPrimary, provider).Price
	return p, p.Set()
}

// parsePrice reads a price, tolerating an empty, decimal-comma or currency-prefixed value.
// Anything unparseable counts as "not configured" rather than as zero cost.
func parsePrice(s string) float64 {
	s = strings.TrimSpace(strings.NewReplacer(",", ".", "$", "", "€", "", "_", "").Replace(s))
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// formatPrice renders a stored price for the Settings screen ("" when unset).
func formatPrice(v float64) string {
	if v <= 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// validatePrice normalizes a price typed in the Settings screen.
func validatePrice(provider, field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	v := parsePrice(s)
	if v == 0 {
		return "", fmt.Errorf("%s %s price %q is not a non-negative number per million tokens", provider, field, s)
	}
	return formatPrice(v), nil
}
