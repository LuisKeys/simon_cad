package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvThenSavedOverride(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s", "settings.json")
	t.Setenv("OPENAI_MODEL", "from-env")
	t.Setenv("SIMONCAD_PROVIDER", "openai")
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	cur := Current()
	if cur.Provider != ProviderOpenAI || cur.Providers[ProviderOpenAI].Model != "from-env" {
		t.Fatalf("env not read: %+v", cur)
	}
	if cur.Providers[ProviderOllama].BaseURL != "http://localhost:11434" || cur.Providers[ProviderClaude].Model != DefaultClaudeModel {
		t.Fatalf("defaults missing: %+v", cur)
	}
	cur.Provider, cur.Theme = ProviderOllama, ThemeLight
	ps := cur.Providers[ProviderOpenAI]
	ps.Model, ps.APIKey, ps.ReasoningEffort = "gpt-x", "sk-1", "high"
	cur.Providers[ProviderOpenAI] = ps
	if err := Save(cur); err != nil {
		t.Fatal(err)
	}
	if Get("OPENAI_MODEL") != "gpt-x" || Get("OPENAI_API_KEY") != "sk-1" || Get("SIMONCAD_PROVIDER") != "ollama" {
		t.Fatal("save did not override env")
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings file perms: %v %v", fi, err)
	}
	if err := LoadFrom(file); err != nil { // a fresh process sees the saved values
		t.Fatal(err)
	}
	if got := Current(); got.Theme != ThemeLight || got.Provider != ProviderOllama || got.Providers[ProviderOpenAI].ReasoningEffort != "high" {
		t.Fatalf("not persisted: %+v", got)
	}
}

func TestSaveRejectsBadValues(t *testing.T) {
	_ = LoadFrom("")
	s := Current()
	s.Theme = "neon"
	if Save(s) == nil {
		t.Fatal("bad theme accepted")
	}
	s = Current()
	s.Providers[ProviderOpenAI] = ProviderSettings{BaseURL: "ftp://x"}
	if Save(s) == nil {
		t.Fatal("bad base URL accepted")
	}
}
