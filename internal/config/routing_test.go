package config

import "testing"

// clearRouting empties the routing variables on top of clean.
func clearRouting(t *testing.T) string {
	t.Helper()
	file := clean(t)
	for _, k := range []string{keyRoutingMode, keyLocalRetries, keyStandardRetries, keyAdvancedEnabled, keyAdvancedProv,
		AdvancedPrefix + "OPENAI_MODEL", AdvancedPrefix + "OPENAI_API_KEY", AdvancedPrefix + "OPENAI_INPUT_PRICE", AdvancedPrefix + "ANTHROPIC_MODEL"} {
		t.Setenv(k, "")
	}
	return file
}

// A configuration that predates routing behaves as it did: manual with a cloud primary, and
// local-first (cloud only when needed and authorized) with a local one.
func TestRoutingModeDefaults(t *testing.T) {
	clearRouting(t)
	if m := RoutingMode(); m != ModeManual {
		t.Fatalf("cloud primary: %s", m)
	}
	t.Setenv("SIMONCAD_PROVIDER", "ollama")
	if m := RoutingMode(); m != ModeLocalFirst {
		t.Fatalf("local primary: %s", m)
	}
	t.Setenv(keyRoutingMode, "local_only")
	if m := RoutingMode(); m != ModeLocalOnly {
		t.Fatalf("explicit: %s", m)
	}
	t.Setenv(keyRoutingMode, "nonsense")
	if m := RoutingMode(); m != ModeLocalFirst {
		t.Fatalf("unknown values are ignored: %s", m)
	}
	if LocalRetries() != 2 || StandardRetries() != 1 {
		t.Fatalf("default limits: %d %d", LocalRetries(), StandardRetries())
	}
	t.Setenv(keyLocalRetries, "4")
	t.Setenv(keyStandardRetries, "x")
	if LocalRetries() != 4 || StandardRetries() != 1 {
		t.Fatalf("limits: %d %d", LocalRetries(), StandardRetries())
	}
}

// The advanced slot is the standard (fallback) model until it is given another one, and a
// price follows its own model.
func TestAdvancedSlotInheritsFromTheFallback(t *testing.T) {
	clearRouting(t)
	t.Setenv("SIMONCAD_PROVIDER", "ollama")
	t.Setenv("OPENAI_API_KEY", "sk-1")
	t.Setenv("OPENAI_MODEL", "gpt-small")
	t.Setenv("OPENAI_INPUT_PRICE", "1")
	mc, enabled := AdvancedModel()
	if enabled || mc.Provider != ProviderOpenAI || mc.Model != "gpt-small" || mc.APIKey != "sk-1" || mc.Price.Input != 1 {
		t.Fatalf("inherited: %+v %v", mc, enabled)
	}
	t.Setenv(keyAdvancedEnabled, "true")
	t.Setenv(AdvancedPrefix+"OPENAI_MODEL", "gpt-big")
	mc, enabled = AdvancedModel()
	if !enabled || mc.Model != "gpt-big" || mc.APIKey != "sk-1" || mc.Price.Set() {
		t.Fatalf("own model keeps the key but not the other model's price: %+v", mc)
	}
	t.Setenv(AdvancedPrefix+"OPENAI_INPUT_PRICE", "5")
	if p, _, ok := PriceForModel(ProviderOpenAI, "gpt-big"); !ok || p.Input != 5 {
		t.Fatalf("the advanced model is priced by its slot: %+v %v", p, ok)
	}
	if p, _, ok := PriceForModel(ProviderOpenAI, "gpt-small"); !ok || p.Input != 1 {
		t.Fatalf("and the standard one by its own: %+v %v", p, ok)
	}
}

func TestRoutingSavesAndReloads(t *testing.T) {
	file := clearRouting(t)
	s := Current()
	s.Routing.Mode, s.Routing.LocalRetries, s.Routing.StandardRetries = ModeLocalOnly, 3, 0
	s.Routing.Advanced.Enabled, s.Routing.Advanced.Provider = true, ProviderOpenAI
	s.Routing.Advanced.Providers[ProviderOpenAI] = ProviderSettings{Model: "gpt-big", InputPrice: "7"}
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	r := Current().Routing
	if r.Mode != ModeLocalOnly || r.LocalRetries != 3 || r.StandardRetries != 0 || !r.Advanced.Enabled || r.Advanced.Providers[ProviderOpenAI].Model != "gpt-big" {
		t.Fatalf("reloaded: %+v", r)
	}
	// a client that does not know routing leaves it alone
	s2 := Current()
	s2.Routing = nil
	if err := Save(s2); err != nil {
		t.Fatal(err)
	}
	if Current().Routing.Mode != ModeLocalOnly {
		t.Fatal("nil routing must keep the saved one")
	}
	bad := Current()
	bad.Routing.Mode = "everywhere"
	if err := Save(bad); err == nil {
		t.Fatal("an unknown mode must be refused")
	}
}
