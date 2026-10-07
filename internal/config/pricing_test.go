package config

import (
	"path/filepath"
	"testing"
)

func TestPriceUnsetMeansTokensOnly(t *testing.T) {
	_ = LoadFrom("")
	t.Setenv("ANTHROPIC_INPUT_PRICE", "")
	t.Setenv("ANTHROPIC_OUTPUT_PRICE", "")
	if p, ok := PriceFor(ProviderClaude); ok || p.Set() {
		t.Fatalf("an unset price must not be configured: %+v", p)
	}
	if _, ok := PriceFor("nope"); ok {
		t.Fatal("unknown provider priced")
	}
}

func TestPriceFromEnvAndCost(t *testing.T) {
	_ = LoadFrom("")
	t.Setenv("ANTHROPIC_INPUT_PRICE", "3")
	t.Setenv("ANTHROPIC_OUTPUT_PRICE", "15")
	p, ok := PriceFor(ProviderClaude)
	if !ok {
		t.Fatal("price not read from the environment")
	}
	// 1M input + 1M output at 3 / 15 per million.
	if got := p.Cost(1_000_000, 0, 1_000_000); got != 18 {
		t.Fatalf("cost = %v, want 18", got)
	}
	if got := p.Cost(1000, 0, 500); got != 3*1000/1e6+15*500/1e6 {
		t.Fatalf("cost = %v", got)
	}
}

func TestOnlyOutputPriceIsStillConfigured(t *testing.T) {
	_ = LoadFrom("")
	t.Setenv("OPENAI_INPUT_PRICE", "")
	t.Setenv("OPENAI_OUTPUT_PRICE", "10")
	p, ok := PriceFor(ProviderOpenAI)
	if !ok || p.Input != 0 || p.Output != 10 {
		t.Fatalf("partial price: %+v ok=%v", p, ok)
	}
}

func TestParsePriceTolerance(t *testing.T) {
	for in, want := range map[string]float64{
		"":        0,
		"  2.5  ": 2.5,
		"2,5":     2.5, // decimal comma
		"$3":      3,
		"abc":     0, // unparseable counts as unset, never as free
		"-1":      0,
	} {
		if got := parsePrice(in); got != want {
			t.Fatalf("parsePrice(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPricesRoundTripThroughSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("ANTHROPIC_INPUT_PRICE", "")
	t.Setenv("ANTHROPIC_OUTPUT_PRICE", "")
	t.Setenv("SIMONCAD_CURRENCY", "")
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	if Currency() != DefaultCurrency {
		t.Fatalf("currency default = %q", Currency())
	}
	s := Current()
	ps := s.Providers[ProviderClaude]
	ps.InputPrice, ps.OutputPrice = "3", "15,0"
	s.Providers[ProviderClaude] = ps
	s.Currency = "eur"
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if err := LoadFrom(file); err != nil { // a fresh process sees them
		t.Fatal(err)
	}
	p, ok := PriceFor(ProviderClaude)
	if !ok || p.Input != 3 || p.Output != 15 {
		t.Fatalf("prices not persisted: %+v ok=%v", p, ok)
	}
	if Currency() != "EUR" {
		t.Fatalf("currency = %q, want EUR", Currency())
	}
	if got := Current().Providers[ProviderClaude]; got.InputPrice != "3" || got.OutputPrice != "15" {
		t.Fatalf("settings view: %+v", got)
	}
}

func TestSaveRejectsBadPrice(t *testing.T) {
	_ = LoadFrom("")
	s := Current()
	ps := s.Providers[ProviderOpenAI]
	ps.InputPrice = "cheap"
	s.Providers[ProviderOpenAI] = ps
	if err := Save(s); err == nil {
		t.Fatal("non-numeric price accepted")
	}
	s = Current()
	s.Currency = "much too long a currency"
	if err := Save(s); err == nil {
		t.Fatal("overlong currency accepted")
	}
}

func TestClearingAPriceDisablesEstimation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("ANTHROPIC_INPUT_PRICE", "")
	t.Setenv("ANTHROPIC_OUTPUT_PRICE", "")
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	s := Current()
	ps := s.Providers[ProviderClaude]
	ps.InputPrice, ps.OutputPrice = "3", "15"
	s.Providers[ProviderClaude] = ps
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	s = Current()
	ps = s.Providers[ProviderClaude]
	ps.InputPrice, ps.OutputPrice = "", ""
	s.Providers[ProviderClaude] = ps
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if p, ok := PriceFor(ProviderClaude); ok {
		t.Fatalf("cleared price still configured: %+v", p)
	}
}

func TestCachedInputPriceRoundTrips(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	for _, k := range []string{"ANTHROPIC_INPUT_PRICE", "ANTHROPIC_OUTPUT_PRICE", "ANTHROPIC_CACHED_INPUT_PRICE"} {
		t.Setenv(k, "")
	}
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	s := Current()
	ps := s.Providers[ProviderClaude]
	ps.InputPrice, ps.OutputPrice, ps.CachedInputPrice = "3", "15", "0.3"
	s.Providers[ProviderClaude] = ps
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	p, ok := PriceFor(ProviderClaude)
	if !ok || p.CachedInput != 0.3 {
		t.Fatalf("cached price not persisted: %+v", p)
	}
	if got := Current().Providers[ProviderClaude].CachedInputPrice; got != "0.3" {
		t.Fatalf("settings view: %q", got)
	}
}

func TestOnlyACachedPriceStillCounts(t *testing.T) {
	_ = LoadFrom("")
	t.Setenv("OPENAI_INPUT_PRICE", "")
	t.Setenv("OPENAI_OUTPUT_PRICE", "")
	t.Setenv("OPENAI_CACHED_INPUT_PRICE", "0.5")
	if _, ok := PriceFor(ProviderOpenAI); !ok {
		t.Fatal("a cached-only price was treated as unconfigured")
	}
}

func TestCachedCostNeverExceedsTheFullRate(t *testing.T) {
	p := Price{Input: 3, Output: 0, CachedInput: 0.3}
	full := Price{Input: 3, Output: 0}
	if got, want := p.Cost(1_000_000, 1_000_000, 0), 0.3; got != want {
		t.Fatalf("all-cached cost = %v, want %v", got, want)
	}
	if p.Cost(1_000_000, 500_000, 0) >= full.Cost(1_000_000, 0, 0) {
		t.Fatal("a half-cached call cost as much as an uncached one")
	}
}
