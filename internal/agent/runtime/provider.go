package runtime

import (
	"errors"
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

// NewNamedProviderFromEnv builds one provider from its primary-slot configuration,
// independently of SIMONCAD_PROVIDER.
func NewNamedProviderFromEnv(name string) (Provider, error) {
	return NewProviderFor(config.ModelConfigFor(config.SlotPrimary, name))
}

// NewProviderFor builds the provider of one model configuration (either slot).
func NewProviderFor(mc config.ModelConfig) (Provider, error) {
	client := &http.Client{Timeout: 300 * time.Second}
	switch mc.Provider {
	case ProviderOpenAI:
		if mc.APIKey == "" {
			return nil, fmt.Errorf("SIMONCAD_PROVIDER=openai but OPENAI_API_KEY is not set")
		}
		if mc.Model == "" {
			return nil, fmt.Errorf("SIMONCAD_PROVIDER=openai but OPENAI_MODEL is not set")
		}
		return &OpenAI{Flavor: "openai", APIKey: mc.APIKey, Model: mc.Model, HTTP: client, ReasoningEffort: mc.ReasoningEffort,
			BaseURL: strings.TrimRight(orDefault(mc.BaseURL, "https://api.openai.com"), "/")}, nil
	case ProviderOllama:
		if mc.Model == "" {
			return nil, fmt.Errorf("SIMONCAD_PROVIDER=ollama but OLLAMA_MODEL is not set (e.g. llama3.1)")
		}
		return &OpenAI{Flavor: "ollama", Model: mc.Model, HTTP: client, NumCtx: mc.NumCtx,
			BaseURL: strings.TrimRight(orDefault(mc.BaseURL, "http://localhost:11434"), "/")}, nil
	case ProviderClaude:
		c, err := newClaude(mc)
		if err != nil {
			return nil, err // not a typed nil inside the interface
		}
		return c, nil
	}
	return nil, fmt.Errorf("unknown provider %q", mc.Provider)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ErrUnavailable matches errors of a provider that could not be reached or kept failing
// after its own retries (network errors, 429, 5xx). It is the recoverable kind of failure:
// the router may hand such a request to the fallback model.
var ErrUnavailable = errors.New("provider unavailable")

type unavailableError struct{ err error }

func (e *unavailableError) Error() string        { return e.err.Error() }
func (e *unavailableError) Unwrap() error        { return e.err }
func (e *unavailableError) Is(target error) bool { return target == ErrUnavailable }

func unavailable(err error) error { return &unavailableError{err} }

// Identifier is implemented by providers that know their provider and model names.
type Identifier interface {
	Identity() (provider, model string)
}

// Identify returns a provider's provider and model names. A provider that does not say
// (test doubles, recordings) reports no provider and its Name as the model.
func Identify(p Provider) (provider, model string) {
	if id, ok := p.(Identifier); ok {
		return id.Identity()
	}
	return "", p.Name()
}

// IsLocal reports whether p runs on this machine.
func IsLocal(p Provider) bool {
	name, _ := Identify(p)
	return config.Local(name)
}
