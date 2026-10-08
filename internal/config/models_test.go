package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// clean starts a test from an empty settings file and no model variables in the environment.
func clean(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"SIMONCAD_PROVIDER", "OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_BASE_URL", "OPENAI_REASONING_EFFORT",
		"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", "OLLAMA_MODEL", "OLLAMA_BASE_URL",
		"OPENAI_INPUT_PRICE", "OPENAI_OUTPUT_PRICE", "OPENAI_CACHED_INPUT_PRICE", "OPENAI_CURRENCY", "SIMONCAD_CURRENCY",
		"ANTHROPIC_INPUT_PRICE", "OLLAMA_INPUT_PRICE", "OLLAMA_NUM_CTX", keyFallbackEnabled, keyFallbackProvider, keyFallbackConsent,
		FallbackPrefix + "OPENAI_MODEL", FallbackPrefix + "OPENAI_API_KEY", FallbackPrefix + "OPENAI_INPUT_PRICE"} {
		t.Setenv(k, "")
	}
	file := filepath.Join(t.TempDir(), "settings.json")
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	return file
}

// A configuration saved before the fallback slot existed keeps working: the first complete
// cloud provider becomes the fallback, enabled, and nothing is rewritten until a save.
func TestFallbackMigratesFromTheExistingConfiguration(t *testing.T) {
	clean(t)
	t.Setenv("SIMONCAD_PROVIDER", "ollama")
	t.Setenv("OLLAMA_MODEL", "qwen3")
	t.Setenv("OPENAI_API_KEY", "sk-old")
	t.Setenv("OPENAI_MODEL", "gpt-x")
	t.Setenv("OPENAI_INPUT_PRICE", "2")
	mc, enabled := FallbackModel()
	if !enabled || mc.Provider != ProviderOpenAI || mc.APIKey != "sk-old" || mc.Model != "gpt-x" || mc.Price.Input != 2 {
		t.Fatalf("migrated fallback: %+v %v", mc, enabled)
	}
	fb := Current().Fallback
	if !fb.Migrated || !fb.Enabled || fb.Provider != ProviderOpenAI || fb.Consent != ConsentAsk || fb.Problem != "" {
		t.Fatalf("view: %+v", fb)
	}
	// no complete cloud provider: fallback is off, and the settings say what is missing
	t.Setenv("OPENAI_API_KEY", "")
	if _, enabled := FallbackModel(); enabled {
		t.Fatal("an incomplete provider is not a fallback")
	}
	if fb := Current().Fallback; fb.Enabled || !strings.Contains(fb.Problem, "API key") {
		t.Fatalf("incomplete: %+v", fb)
	}
}

// Primary and fallback are configured, saved and reloaded independently: changing the
// primary never touches the fallback, and keys survive provider switches.
func TestPrimaryAndFallbackAreIndependent(t *testing.T) {
	file := clean(t)
	s := Current()
	s.Provider = ProviderOllama
	s.Providers[ProviderOllama] = ProviderSettings{Model: "qwen3", InputPrice: "9"} // an older client's Ollama price
	s.Providers[ProviderOpenAI] = ProviderSettings{APIKey: "sk-primary", Model: "gpt-small", InputPrice: "1"}
	s.Fallback = &FallbackSettings{Enabled: true, Provider: ProviderOpenAI, Consent: ConsentAllow, Providers: map[string]ProviderSettings{
		ProviderOpenAI: {APIKey: "sk-fallback", Model: "gpt-big", InputPrice: "10", Currency: "eur"},
		ProviderClaude: {APIKey: "sk-ant", Model: "claude-x"},
	}}
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if err := LoadFrom(file); err != nil { // as after a restart
		t.Fatal(err)
	}
	got := Current()
	if got.Providers[ProviderOllama].InputPrice != "" || got.Providers[ProviderOllama].Currency != "" {
		t.Fatalf("local models are never priced: %+v", got.Providers[ProviderOllama])
	}
	fo := got.Fallback.Providers[ProviderOpenAI]
	if !got.Fallback.Enabled || got.Fallback.Migrated || got.Fallback.Consent != ConsentAllow || fo.APIKey != "sk-fallback" ||
		fo.Model != "gpt-big" || fo.InputPrice != "10" || fo.Currency != "EUR" {
		t.Fatalf("fallback not persisted: %+v %+v", got.Fallback, fo)
	}
	// switch the primary to OpenAI with another key: the fallback keeps its own
	got.Provider = ProviderOpenAI
	got.Providers[ProviderOpenAI] = ProviderSettings{APIKey: "sk-new", Model: "gpt-small"}
	got.Fallback = nil // a client that does not know the fallback leaves it alone
	if err := Save(got); err != nil {
		t.Fatal(err)
	}
	if mc, on := FallbackModel(); !on || mc.APIKey != "sk-fallback" || mc.Model != "gpt-big" {
		t.Fatalf("primary change touched the fallback: %+v", mc)
	}
	// switching the fallback provider keeps the other provider's configuration
	got = Current()
	got.Fallback.Provider = ProviderClaude
	if err := Save(got); err != nil {
		t.Fatal(err)
	}
	if mc, _ := FallbackModel(); mc.Provider != ProviderClaude || mc.APIKey != "sk-ant" {
		t.Fatalf("claude fallback: %+v", mc)
	}
	if mc := ModelConfigFor(SlotFallback, ProviderOpenAI); mc.APIKey != "sk-fallback" {
		t.Fatalf("openai fallback lost: %+v", mc)
	}
	// prices follow the model that ran
	if p, cur, ok := PriceForModel(ProviderOpenAI, "gpt-big"); !ok || p.Input != 10 || cur != "EUR" {
		t.Fatalf("fallback price: %+v %s %v", p, cur, ok)
	}
	if _, _, ok := PriceForModel(ProviderOllama, "qwen3"); ok {
		t.Fatal("ollama priced")
	}
}

func TestSaveRejectsALocalFallback(t *testing.T) {
	clean(t)
	s := Current()
	s.Fallback.Provider = ProviderOllama
	if err := Save(s); err == nil {
		t.Fatal("ollama accepted as fallback")
	}
	s = Current()
	s.Fallback.Enabled, s.Fallback.Provider = true, ProviderOpenAI
	if err := Save(s); err == nil || !strings.Contains(err.Error(), "saved, but the fallback model cannot be used") {
		t.Fatalf("incomplete enabled fallback: %v", err)
	}
}

func TestSetFallbackConsentPersists(t *testing.T) {
	file := clean(t)
	if err := SetFallbackConsent(ConsentAllow); err != nil {
		t.Fatal(err)
	}
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	if FallbackConsent() != ConsentAllow {
		t.Fatal("consent not persisted")
	}
	if SetFallbackConsent("maybe") == nil {
		t.Fatal("bad consent accepted")
	}
}

// The Ollama context window defaults to what a CAD turn needs, 0 hands it to the server, and
// only sensible windows are saved.
func TestOllamaContextLength(t *testing.T) {
	clean(t)
	t.Setenv("OLLAMA_NUM_CTX", "")
	if mc := ModelConfigFor(SlotPrimary, ProviderOllama); mc.NumCtx != DefaultContextLength {
		t.Fatalf("default: %d", mc.NumCtx)
	}
	s := Current()
	s.Providers[ProviderOllama] = ProviderSettings{Model: "qwen3", ContextLength: "65536"}
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if mc := ModelConfigFor(SlotPrimary, ProviderOllama); mc.NumCtx != 65536 || Current().Providers[ProviderOllama].ContextLength != "65536" {
		t.Fatalf("saved: %+v", mc)
	}
	s.Providers[ProviderOllama] = ProviderSettings{Model: "qwen3", ContextLength: "0"}
	if err := Save(s); err != nil || ModelConfigFor(SlotPrimary, ProviderOllama).NumCtx != 0 {
		t.Fatalf("server default: %v", err)
	}
	for _, bad := range []string{"1024", "-1", "lots"} {
		s.Providers[ProviderOllama] = ProviderSettings{Model: "qwen3", ContextLength: bad}
		if Save(s) == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if ModelConfigFor(SlotPrimary, ProviderOpenAI).NumCtx != 0 {
		t.Fatal("cloud models have no window setting")
	}
}
