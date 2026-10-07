// Package config is the central configuration layer. Values start from the process
// environment (including .env) and are overridden by the settings saved from the UI, which
// persist in a JSON file under the user's config directory. Application code reads values
// through Get (or Current) instead of os.Getenv, so a saved setting takes effect on the next
// agent turn without a restart.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Provider names (SIMONCAD_PROVIDER values).
const (
	ProviderClaude = "claude"
	ProviderOpenAI = "openai"
	ProviderOllama = "ollama"
)

// Providers lists the selectable providers.
var Providers = []string{ProviderOpenAI, ProviderClaude, ProviderOllama}

// DefaultClaudeModel is used when ANTHROPIC_MODEL is not set.
const DefaultClaudeModel = "claude-sonnet-5-5"

// Themes selectable in the UI.
const (
	ThemeLight = "light"
	ThemeDark  = "dark"
)

// ProviderSettings is the configuration of one provider. APIKey is unused for Ollama and
// ReasoningEffort only applies to OpenAI.
type ProviderSettings struct {
	APIKey          string `json:"apiKey"`
	Model           string `json:"model"`
	BaseURL         string `json:"baseUrl"`
	ReasoningEffort string `json:"reasoningEffort"`
}

// Settings is the view of the configuration the Settings screen edits.
type Settings struct {
	Provider  string                      `json:"provider"`
	Providers map[string]ProviderSettings `json:"providers"`
	Theme     string                      `json:"theme"`
}

// Env variable names per provider.
var envKeys = map[string]struct{ key, model, baseURL, effort string }{
	ProviderOpenAI: {"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_BASE_URL", "OPENAI_REASONING_EFFORT"},
	ProviderClaude: {"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", ""},
	ProviderOllama: {"", "OLLAMA_MODEL", "OLLAMA_BASE_URL", ""},
}

// DefaultBaseURL is the base URL of a provider when none is configured.
func DefaultBaseURL(provider string) string {
	switch provider {
	case ProviderOpenAI:
		return "https://api.openai.com"
	case ProviderClaude:
		return "https://api.anthropic.com"
	case ProviderOllama:
		return "http://localhost:11434"
	}
	return ""
}

const (
	keyProvider = "SIMONCAD_PROVIDER"
	keyTheme    = "SIMONCAD_THEME"
)

var (
	mu        sync.RWMutex
	overrides = map[string]string{}
	path      string
)

// Path returns the settings file: SIMONCAD_CONFIG, else <user config dir>/simoncad/settings.json.
func Path() string {
	if p := os.Getenv("SIMONCAD_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "simoncad", "settings.json")
}

// Load reads the saved settings from Path(); a missing file is not an error.
func Load() error { return LoadFrom(Path()) }

// LoadFrom reads saved settings from file and makes them the active overrides.
func LoadFrom(file string) error {
	mu.Lock()
	defer mu.Unlock()
	path = file
	overrides = map[string]string{}
	if file == "" {
		return nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(b, &overrides); err != nil {
		overrides = map[string]string{}
		return fmt.Errorf("settings %s: %w", file, err)
	}
	return nil
}

// Get returns a configuration value: the saved setting if there is one, else the environment.
func Get(key string) string {
	mu.RLock()
	v, ok := overrides[key]
	mu.RUnlock()
	if !ok {
		v = os.Getenv(key)
	}
	return strings.TrimSpace(v)
}

// GetOr is Get with a default for empty values.
func GetOr(key, def string) string {
	if v := Get(key); v != "" {
		return v
	}
	return def
}

// ProviderName returns the normalized selected provider (default claude).
func ProviderName() (string, error) {
	switch p := strings.ToLower(GetOr(keyProvider, ProviderClaude)); p {
	case "claude", "anthropic":
		return ProviderClaude, nil
	case "openai":
		return ProviderOpenAI, nil
	case "ollama":
		return ProviderOllama, nil
	default:
		return "", fmt.Errorf("unknown SIMONCAD_PROVIDER %q (use claude, openai or ollama)", p)
	}
}

// Current returns the effective settings, with defaults filled in for display.
func Current() Settings {
	s := Settings{Provider: ProviderClaude, Providers: map[string]ProviderSettings{}, Theme: ThemeDark}
	if p, err := ProviderName(); err == nil {
		s.Provider = p
	}
	if GetOr(keyTheme, ThemeDark) == ThemeLight {
		s.Theme = ThemeLight
	}
	for _, p := range Providers {
		k := envKeys[p]
		ps := ProviderSettings{BaseURL: GetOr(k.baseURL, DefaultBaseURL(p)), Model: Get(k.model)}
		if k.key != "" {
			ps.APIKey = Get(k.key)
		}
		if k.effort != "" {
			ps.ReasoningEffort = strings.ToLower(Get(k.effort))
		}
		if p == ProviderClaude && ps.Model == "" {
			ps.Model = DefaultClaudeModel
		}
		s.Providers[p] = ps
	}
	return s
}

// Save validates s, makes it the active configuration and persists it.
func Save(s Settings) error {
	if _, ok := envKeys[s.Provider]; !ok {
		return fmt.Errorf("unknown provider %q", s.Provider)
	}
	if s.Theme != ThemeLight && s.Theme != ThemeDark {
		return fmt.Errorf("unknown theme %q", s.Theme)
	}
	next := map[string]string{keyProvider: s.Provider, keyTheme: s.Theme}
	for _, p := range Providers {
		ps, ok := s.Providers[p]
		if !ok {
			continue
		}
		k := envKeys[p]
		switch ps.ReasoningEffort {
		case "", "none", "low", "medium", "high":
		default:
			return fmt.Errorf("unknown reasoning effort %q", ps.ReasoningEffort)
		}
		if u := strings.TrimSpace(ps.BaseURL); u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("%s base URL must start with http:// or https://", p)
		}
		if k.key != "" {
			next[k.key] = strings.TrimSpace(ps.APIKey)
		}
		next[k.model] = strings.TrimSpace(ps.Model)
		next[k.baseURL] = strings.TrimSpace(ps.BaseURL)
		if k.effort != "" {
			next[k.effort] = ps.ReasoningEffort
		}
	}
	mu.Lock()
	defer mu.Unlock()
	merged := map[string]string{}
	for k, v := range overrides {
		merged[k] = v
	}
	for k, v := range next {
		merged[k] = v
	}
	if path != "" {
		b, _ := json.MarshalIndent(merged, "", "  ")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil { // holds API keys
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	overrides = merged
	return nil
}
