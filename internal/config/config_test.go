package config

import (
	"os"
	"path/filepath"
	"strings"
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

// A field cleared in the Settings screen must fall back to the environment instead of
// shadowing it: the first save from the UI used to write "" for every untouched field,
// which silently disabled whatever the user had configured in .env.
func TestClearedSettingFallsBackToTheEnvironment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("SIMONCAD_COMPONENT_LIBRARY", "/from/env")
	t.Setenv("OPENAI_MODEL", "model-from-env")
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	s := Current()
	s.ComponentLibrary = "" // never touched in the UI
	ps := s.Providers[ProviderOpenAI]
	ps.Model = ""
	s.Providers[ProviderOpenAI] = ps
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if got := ComponentLibrary(); got != "/from/env" {
		t.Fatalf("component library = %q, want the environment value", got)
	}
	if got := Get("OPENAI_MODEL"); got != "model-from-env" {
		t.Fatalf("model = %q, want the environment value", got)
	}
	// And it survives a reload: the empty key is not written to the file at all.
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	if got := ComponentLibrary(); got != "/from/env" {
		t.Fatalf("after reload: %q", got)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"SIMONCAD_COMPONENT_LIBRARY"`) {
		t.Fatalf("the empty key was persisted: %s", b)
	}
}

// A non-empty saved value still wins over the environment.
func TestSavedValueStillOverridesTheEnvironment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("OPENAI_MODEL", "model-from-env")
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	s := Current()
	ps := s.Providers[ProviderOpenAI]
	ps.Model = "model-from-ui"
	s.Providers[ProviderOpenAI] = ps
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if got := Get("OPENAI_MODEL"); got != "model-from-ui" {
		t.Fatalf("model = %q", got)
	}
}
