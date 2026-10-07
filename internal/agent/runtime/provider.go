package runtime

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/config"
)

// Providers selectable with SIMONCAD_PROVIDER.
const (
	ProviderClaude = "claude"
	ProviderOpenAI = "openai"
	ProviderOllama = "ollama"
)

func envOr(key, def string) string { return config.GetOr(key, def) }

// ProviderNameFromEnv returns the normalized SIMONCAD_PROVIDER (default claude).
func ProviderNameFromEnv() (string, error) {
	return config.ProviderName()
}

// NewProviderFromEnv builds the live LLM provider chosen by SIMONCAD_PROVIDER:
//
//	claude: ANTHROPIC_API_KEY, ANTHROPIC_MODEL, ANTHROPIC_BASE_URL
//	openai: OPENAI_API_KEY,    OPENAI_MODEL,    OPENAI_BASE_URL
//	ollama: OLLAMA_MODEL,      OLLAMA_BASE_URL  (no key)
func NewProviderFromEnv() (Provider, error) {
	name, err := ProviderNameFromEnv()
	if err != nil {
		return nil, err
	}
	return NewNamedProviderFromEnv(name)
}

// NewNamedProviderFromEnv builds one provider from its environment configuration,
// independently of SIMONCAD_PROVIDER (the router uses it for fallbacks).
func NewNamedProviderFromEnv(name string) (Provider, error) {
	client := &http.Client{Timeout: 300 * time.Second}
	switch name {
	case ProviderOpenAI:
		key := config.Get("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("SIMONCAD_PROVIDER=openai but OPENAI_API_KEY is not set")
		}
		model := config.Get("OPENAI_MODEL")
		if model == "" {
			return nil, fmt.Errorf("SIMONCAD_PROVIDER=openai but OPENAI_MODEL is not set")
		}
		return &OpenAI{Flavor: "openai", APIKey: key, Model: model, HTTP: client, ReasoningEffort: config.Get("OPENAI_REASONING_EFFORT"),
			BaseURL: strings.TrimRight(envOr("OPENAI_BASE_URL", "https://api.openai.com"), "/")}, nil
	case ProviderOllama:
		model := config.Get("OLLAMA_MODEL")
		if model == "" {
			return nil, fmt.Errorf("SIMONCAD_PROVIDER=ollama but OLLAMA_MODEL is not set (e.g. llama3.1)")
		}
		return &OpenAI{Flavor: "ollama", Model: model, HTTP: client,
			BaseURL: strings.TrimRight(envOr("OLLAMA_BASE_URL", "http://localhost:11434"), "/")}, nil
	case ProviderClaude:
		c, err := NewClaudeFromEnv()
		if err != nil {
			return nil, err // not a typed nil inside the interface
		}
		return c, nil
	}
	return nil, fmt.Errorf("unknown provider %q", name)
}
