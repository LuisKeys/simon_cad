package runtime

import (
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/config"
)

// noPrices clears every price so a test starts from "tokens only".
func noPrices(t *testing.T) {
	t.Helper()
	if err := config.LoadFrom(""); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ANTHROPIC_INPUT_PRICE", "ANTHROPIC_OUTPUT_PRICE", "OPENAI_INPUT_PRICE",
		"OPENAI_OUTPUT_PRICE", "OLLAMA_INPUT_PRICE", "OLLAMA_OUTPUT_PRICE", "SIMONCAD_CURRENCY",
		"ANTHROPIC_CACHED_INPUT_PRICE", "OPENAI_CACHED_INPUT_PRICE", "OLLAMA_CACHED_INPUT_PRICE"} {
		t.Setenv(k, "")
	}
}

func TestUsageAddMergesPerModel(t *testing.T) {
	var u Usage
	u.Add(usageFor(ProviderClaude, "sonnet", 100, 0, 10))
	u.Add(usageFor(ProviderClaude, "sonnet", 50, 0, 5))
	u.Add(usageFor(ProviderOpenAI, "gpt-x", 7, 0, 3))
	if u.InputTokens != 157 || u.OutputTokens != 18 || u.Tokens() != 175 {
		t.Fatalf("totals: %+v", u)
	}
	m := u.ByModel[ModelKey(ProviderClaude, "sonnet")]
	if m.InputTokens != 150 || m.OutputTokens != 15 || m.Calls != 2 {
		t.Fatalf("claude breakdown: %+v", m)
	}
	models := u.Models() // heaviest first
	if len(models) != 2 || models[0].Model != "sonnet" || models[1].Model != "gpt-x" {
		t.Fatalf("models: %+v", models)
	}
}

func TestUsageAddDoesNotAliasTheSource(t *testing.T) {
	src := usageFor(ProviderClaude, "sonnet", 10, 0, 1)
	var u Usage
	u.Add(src)
	u.Add(usageFor(ProviderClaude, "sonnet", 10, 0, 1))
	if got := src.ByModel[ModelKey(ProviderClaude, "sonnet")]; got.InputTokens != 10 || got.Calls != 1 {
		t.Fatalf("the source usage was mutated: %+v", got)
	}
}

func TestEstimateWithoutPricesReportsTokensOnly(t *testing.T) {
	noPrices(t)
	u := usageFor(ProviderClaude, "sonnet", 1000, 0, 100)
	c := Estimate(u)
	if c.Known || c.Amount != 0 {
		t.Fatalf("cost invented without prices: %+v", c)
	}
	if len(c.Unpriced) != 1 || c.Unpriced[0] != "sonnet" {
		t.Fatalf("unpriced models: %+v", c.Unpriced)
	}
	if r := Report(u); !strings.Contains(r, "1100 tokens") || !strings.Contains(r, "no prices configured") {
		t.Fatalf("report = %q", r)
	}
}

func TestEstimateWithPrices(t *testing.T) {
	noPrices(t)
	t.Setenv("ANTHROPIC_INPUT_PRICE", "3")
	t.Setenv("ANTHROPIC_OUTPUT_PRICE", "15")
	u := usageFor(ProviderClaude, "sonnet", 1_000_000, 0, 100_000)
	c := Estimate(u)
	if !c.Known || c.Partial || c.Amount != 4.5 { // 3 + 1.5
		t.Fatalf("cost: %+v", c)
	}
	if c.Currency != "USD" {
		t.Fatalf("currency: %q", c.Currency)
	}
	if r := Report(u); !strings.Contains(r, "est. USD 4.5000") {
		t.Fatalf("report = %q", r)
	}
}

func TestEstimateIsPartialWhenOneModelHasNoPrice(t *testing.T) {
	noPrices(t)
	t.Setenv("ANTHROPIC_INPUT_PRICE", "3")
	t.Setenv("ANTHROPIC_OUTPUT_PRICE", "15")
	var u Usage
	u.Add(usageFor(ProviderClaude, "sonnet", 1_000_000, 0, 0))
	u.Add(usageFor(ProviderOpenAI, "gpt-x", 500, 0, 500))
	c := Estimate(u)
	if !c.Known || !c.Partial || c.Amount != 3 {
		t.Fatalf("partial cost: %+v", c)
	}
	if !strings.Contains(c.String(), "partial: no price for gpt-x") {
		t.Fatalf("string = %q", c.String())
	}
}

// Local models are not spend: their tokens are reported, never priced, and do not make a
// priced estimate "partial" — even when an older settings file still holds Ollama prices.
func TestLocalUsageIsNeverCosted(t *testing.T) {
	noPrices(t)
	t.Setenv("ANTHROPIC_INPUT_PRICE", "3")
	t.Setenv("OLLAMA_INPUT_PRICE", "100")
	var u Usage
	u.Add(usageFor(ProviderOllama, "llama", 1_000_000, 0, 500))
	if c := Estimate(u); c.Known || c.Partial || c.Amount != 0 || c.LocalTokens != 1_000_500 || len(c.Unpriced) != 0 {
		t.Fatalf("local only: %+v", c)
	}
	if r := Report(u); !strings.Contains(r, "local model, no cost") {
		t.Fatalf("report = %q", r)
	}
	u.Add(usageFor(ProviderClaude, "claude-sonnet-5-5", 1_000_000, 0, 0))
	c := Estimate(u)
	if !c.Known || c.Partial || c.Amount != 3 || c.CloudTokens != 1_000_000 {
		t.Fatalf("mixed: %+v", c)
	}
	if r := Report(u); !strings.Contains(r, "est. USD 3.0000 for 1000000 cloud tokens; 1000500 local tokens, no cost") {
		t.Fatalf("report = %q", r)
	}
}

// Each model is priced by the slot that configures it: a fallback running another model of
// the same provider uses its own prices, not the primary's.
func TestEstimateUsesTheModelThatRan(t *testing.T) {
	noPrices(t)
	t.Setenv("SIMONCAD_PROVIDER", "openai")
	t.Setenv("OPENAI_MODEL", "gpt-small")
	t.Setenv("OPENAI_INPUT_PRICE", "1")
	t.Setenv("SIMONCAD_FALLBACK_OPENAI_MODEL", "gpt-big")
	t.Setenv("SIMONCAD_FALLBACK_OPENAI_INPUT_PRICE", "10")
	t.Setenv("SIMONCAD_FALLBACK_OPENAI_CURRENCY", "eur")
	var u Usage
	u.Add(usageFor(ProviderOpenAI, "gpt-small", 1_000_000, 0, 0))
	u.Add(usageFor(ProviderOpenAI, "gpt-big", 1_000_000, 0, 0))
	c := Estimate(u)
	if !c.Known || c.Amounts["USD"] != 1 || c.Amounts["EUR"] != 10 || c.String() != "EUR 10.0000 + USD 1.0000" {
		t.Fatalf("per-model prices: %+v %q", c, c.String())
	}
}

func TestUnattributedUsageIsNotCosted(t *testing.T) {
	noPrices(t)
	t.Setenv("ANTHROPIC_INPUT_PRICE", "3")
	// A recorded/stubbed response carries totals but no model attribution.
	u := Usage{InputTokens: 1_000_000, OutputTokens: 0}
	if c := Estimate(u); c.Known || c.Amount != 0 {
		t.Fatalf("unattributed usage was costed: %+v", c)
	}
	if r := Report(u); !strings.Contains(r, "1000000 tokens") {
		t.Fatalf("report = %q", r)
	}
}

func TestCurrencyIsALabel(t *testing.T) {
	noPrices(t)
	t.Setenv("SIMONCAD_CURRENCY", "ars")
	t.Setenv("ANTHROPIC_INPUT_PRICE", "1000")
	c := Estimate(usageFor(ProviderClaude, "sonnet", 1_000_000, 0, 0))
	if c.Currency != "ARS" || c.Amount != 1000 {
		t.Fatalf("cost: %+v", c)
	}
}

func TestCachedInputIsChargedAtItsOwnRate(t *testing.T) {
	noPrices(t)
	t.Setenv("ANTHROPIC_INPUT_PRICE", "3")
	t.Setenv("ANTHROPIC_CACHED_INPUT_PRICE", "0.3")
	t.Setenv("ANTHROPIC_OUTPUT_PRICE", "15")
	// 1M input of which 900k came from the cache.
	u := usageFor(ProviderClaude, "sonnet", 1_000_000, 900_000, 0)
	if u.FreshInputTokens() != 100_000 {
		t.Fatalf("fresh input = %d", u.FreshInputTokens())
	}
	c := Estimate(u)
	if want := 0.1*3 + 0.9*0.3; c.Amount != want {
		t.Fatalf("cost = %v, want %v", c.Amount, want)
	}
}

func TestCachedInputFallsBackToTheFullRate(t *testing.T) {
	noPrices(t)
	t.Setenv("ANTHROPIC_INPUT_PRICE", "3") // no cached rate configured
	// Without a cached price the spend must not be discounted on a guess.
	c := Estimate(usageFor(ProviderClaude, "sonnet", 1_000_000, 900_000, 0))
	if c.Amount != 3 {
		t.Fatalf("cost = %v, want the undiscounted 3", c.Amount)
	}
}

func TestCachedTokensAreNeverCountedTwice(t *testing.T) {
	// Cached input is a subset of the input, so it must not inflate the token total.
	u := usageFor(ProviderClaude, "sonnet", 1000, 800, 100)
	if u.Tokens() != 1100 {
		t.Fatalf("tokens = %d, want 1100", u.Tokens())
	}
	// A provider that over-reports is clamped rather than producing negative fresh input.
	if got := usageFor(ProviderClaude, "sonnet", 100, 500, 0); got.CachedInputTokens != 100 || got.FreshInputTokens() != 0 {
		t.Fatalf("not clamped: %+v", got)
	}
}

func TestReportMentionsTheCachedShare(t *testing.T) {
	noPrices(t)
	if r := Report(usageFor(ProviderClaude, "sonnet", 1000, 800, 10)); !strings.Contains(r, "800 of the input cached") {
		t.Fatalf("report = %q", r)
	}
	if r := Report(usageFor(ProviderClaude, "sonnet", 1000, 0, 10)); strings.Contains(r, "cached") {
		t.Fatalf("a cache note appeared with nothing cached: %q", r)
	}
}
