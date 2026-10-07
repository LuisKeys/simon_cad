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

// ReasoningEfforts are the OpenAI reasoning levels that may be configured, weakest first.
// Which of them a given model accepts varies per model and changes over time, so this is the
// set the application knows how to send, not a promise that every model takes all of them;
// the provider adapts when a model names the subset it accepts.
var ReasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// ValidReasoningEffort reports whether s is a configurable effort ("" = the model default).
func ValidReasoningEffort(s string) bool {
	if s == "" {
		return true
	}
	for _, e := range ReasoningEfforts {
		if e == s {
			return true
		}
	}
	return false
}

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
	// InputPrice and OutputPrice are the cost per million tokens, in Settings.Currency.
	// They are optional: empty means the provider's spend is reported in tokens only.
	// CachedInputPrice is the reduced rate for input served from the provider's prompt
	// cache; empty charges cached input at InputPrice.
	InputPrice       string `json:"inputPrice"`
	OutputPrice      string `json:"outputPrice"`
	CachedInputPrice string `json:"cachedInputPrice"`
}

// Settings is the view of the configuration the Settings screen edits.
type Settings struct {
	Provider  string                      `json:"provider"`
	Providers map[string]ProviderSettings `json:"providers"`
	Theme     string                      `json:"theme"`
	// ComponentLibrary is the directory searched for reusable projects, on top of the ones
	// next to the open project. It is a path, not a URL, and empty means "only the project's
	// own neighbourhood".
	ComponentLibrary string `json:"componentLibrary"`
	// Currency labels the per-provider token prices; it is a label, not a conversion.
	Currency string `json:"currency"`
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
	keyProvider  = "SIMONCAD_PROVIDER"
	keyTheme     = "SIMONCAD_THEME"
	keyComponent = "SIMONCAD_COMPONENT_LIBRARY"
)

// ComponentLibrary is the configured component library root ("" when unset).
func ComponentLibrary() string { return Get(keyComponent) }

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
	s.ComponentLibrary = Get(keyComponent)
	s.Currency = Currency()
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
		pr, _ := PriceFor(p)
		ps.InputPrice, ps.OutputPrice = formatPrice(pr.Input), formatPrice(pr.Output)
		ps.CachedInputPrice = formatPrice(pr.CachedInput)
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
	lib := strings.TrimSpace(s.ComponentLibrary)
	if lib != "" && !filepath.IsAbs(lib) {
		return fmt.Errorf("the component library path must be absolute")
	}
	cur := strings.ToUpper(strings.TrimSpace(s.Currency))
	if cur == "" {
		cur = DefaultCurrency
	}
	if len(cur) > 8 {
		return fmt.Errorf("currency %q is too long (use a short code such as USD)", s.Currency)
	}
	next := map[string]string{keyProvider: s.Provider, keyTheme: s.Theme, keyComponent: lib, keyCurrency: cur}
	for _, p := range Providers {
		ps, ok := s.Providers[p]
		if !ok {
			continue
		}
		k := envKeys[p]
		if !ValidReasoningEffort(ps.ReasoningEffort) {
			return fmt.Errorf("unknown reasoning effort %q (use one of: %s)", ps.ReasoningEffort, strings.Join(ReasoningEfforts, ", "))
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
		in, err := validatePrice(p, "input", ps.InputPrice)
		if err != nil {
			return err
		}
		out, err := validatePrice(p, "output", ps.OutputPrice)
		if err != nil {
			return err
		}
		cached, err := validatePrice(p, "cached input", ps.CachedInputPrice)
		if err != nil {
			return err
		}
		pk := priceKeys[p]
		next[pk.in], next[pk.out], next[pk.cached] = in, out, cached
	}
	mu.Lock()
	defer mu.Unlock()
	merged := map[string]string{}
	for k, v := range overrides {
		merged[k] = v
	}
	// A field left empty in the Settings screen means "not set here", not "set to empty":
	// it is removed rather than stored, so the value falls back to the environment (.env)
	// the way an unsaved installation behaves. Storing "" would shadow the environment for
	// good, and the first save from the UI would silently disable every variable the user
	// had only ever configured in .env.
	for k, v := range next {
		if strings.TrimSpace(v) == "" {
			delete(merged, k)
			continue
		}
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
