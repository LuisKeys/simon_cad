package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Model slots. The primary model serves every request it can; the fallback model (a cloud
// provider) serves what the primary cannot, when it is enabled.
//
// The primary slot is stored in the provider variables SimonCAD has always used (OPENAI_MODEL,
// ANTHROPIC_API_KEY...), so an existing configuration is the primary configuration unchanged.
// The fallback slot has its own variables, FallbackPrefix + the same names; a fallback value
// that was never set inherits the provider's primary-slot value, which is how an existing
// OpenAI or Anthropic configuration becomes the fallback without being copied or rewritten.
//
// The fallback slot is the Cloud Standard tier of the router. The advanced slot (Cloud Advanced)
// inherits from the fallback slot the same way, with its own variables under AdvancedPrefix, so
// it is the same model until the user gives it another one.
const (
	SlotPrimary  = "primary"
	SlotFallback = "fallback"
	SlotAdvanced = "advanced"
)

// FallbackPrefix prefixes the provider variables of the fallback slot.
const FallbackPrefix = "SIMONCAD_FALLBACK_"

// AdvancedPrefix prefixes the provider variables of the advanced slot.
const AdvancedPrefix = "SIMONCAD_ADVANCED_"

const (
	keyFallbackEnabled  = "SIMONCAD_FALLBACK_ENABLED"
	keyFallbackProvider = "SIMONCAD_FALLBACK_PROVIDER"
	keyFallbackConsent  = "SIMONCAD_FALLBACK_CONSENT"
)

// FallbackProviders are the providers the fallback slot accepts (cloud only), in the
// priority order used to migrate a configuration that predates the fallback slot.
var FallbackProviders = []string{ProviderOpenAI, ProviderClaude}

// Consent to send a local-primary request to the remote fallback.
const (
	ConsentAsk   = "ask"   // ask before the first remote fallback (default)
	ConsentAllow = "allow" // authorized
	ConsentDeny  = "deny"  // never send to the remote fallback
)

// Local reports whether a provider runs on this machine. Local models are never priced and
// sending their requests to a remote fallback needs the user's consent.
func Local(provider string) bool { return provider == ProviderOllama }

// ModelConfig is the effective configuration of one model in one slot.
type ModelConfig struct {
	Provider        string
	Model           string
	APIKey          string
	BaseURL         string // "" = the provider's default
	ReasoningEffort string
	// Price is zero for local models and when no price is configured.
	Price    Price
	Currency string
	// NumCtx is the context window asked of a local model (0 = the server's default).
	NumCtx int
}

// DefaultContextLength is the context window asked of Ollama when none is configured. A CAD
// agent turn needs ~16k tokens before any conversation (system prompt and tool definitions),
// and Ollama's own default (4096) silently drops what does not fit.
const DefaultContextLength = 32768

// MinContextLength is the smallest context window that may be configured.
const MinContextLength = 2048

// Complete is nil when the configuration is enough to call the model, else what is missing.
func (m ModelConfig) Complete() error {
	if _, ok := envKeys[m.Provider]; !ok {
		return fmt.Errorf("unknown provider %q", m.Provider)
	}
	if envKeys[m.Provider].key != "" && m.APIKey == "" {
		return fmt.Errorf("%s API key is not set", ProviderLabel(m.Provider))
	}
	if m.Model == "" {
		return fmt.Errorf("%s model is not set", ProviderLabel(m.Provider))
	}
	return nil
}

// ProviderLabel is the display name of a provider.
func ProviderLabel(provider string) string {
	switch provider {
	case ProviderOpenAI:
		return "OpenAI"
	case ProviderClaude:
		return "Anthropic"
	case ProviderOllama:
		return "Ollama"
	}
	return provider
}

// currencyKeys label each provider's prices; empty falls back to SIMONCAD_CURRENCY.
var currencyKeys = map[string]string{
	ProviderOpenAI: "OPENAI_CURRENCY",
	ProviderClaude: "ANTHROPIC_CURRENCY",
}

// ModelConfigFor returns a provider's configuration in a slot.
func ModelConfigFor(slot, provider string) ModelConfig {
	k, ok := envKeys[provider]
	if !ok {
		return ModelConfig{Provider: provider}
	}
	get := func(key string) string {
		if key == "" {
			return ""
		}
		return Get(key)
	}
	pk := priceKeys[provider]
	mc := ModelConfig{Provider: provider, Model: get(k.model), APIKey: get(k.key), BaseURL: get(k.baseURL),
		ReasoningEffort: strings.ToLower(get(k.effort)), Currency: get(currencyKeys[provider]),
		Price: Price{Input: parsePrice(Get(pk.in)), Output: parsePrice(Get(pk.out)), CachedInput: parsePrice(Get(pk.cached))}}
	if provider == ProviderClaude && mc.Model == "" {
		mc.Model = DefaultClaudeModel
	}
	switch slot {
	case SlotFallback:
		mc = overlaySlot(mc, FallbackPrefix)
	case SlotAdvanced:
		mc = overlaySlot(overlaySlot(mc, FallbackPrefix), AdvancedPrefix)
	}
	if k.ctx != "" {
		mc.NumCtx = DefaultContextLength
		if n, err := strconv.Atoi(Get(k.ctx)); err == nil && n >= 0 {
			mc.NumCtx = n
		}
	}
	if Local(provider) {
		mc.Price, mc.Currency = Price{}, "" // local models have no token price
	} else if mc.Currency == "" {
		mc.Currency = Currency()
	}
	mc.Currency = strings.ToUpper(mc.Currency)
	return mc
}

// overlaySlot applies the variables of a slot (named by prefix) on top of base, the slot it
// inherits from: a value that was never set in the slot keeps base's.
func overlaySlot(base ModelConfig, prefix string) ModelConfig {
	k, pk := envKeys[base.Provider], priceKeys[base.Provider]
	mc := base
	inherit := func(key, inherited string) string {
		if key == "" {
			return inherited
		}
		if v := Get(prefix + key); v != "" {
			return v
		}
		return inherited
	}
	mc.Model, mc.APIKey, mc.BaseURL = inherit(k.model, base.Model), inherit(k.key, base.APIKey), inherit(k.baseURL, base.BaseURL)
	mc.ReasoningEffort = strings.ToLower(inherit(k.effort, base.ReasoningEffort))
	mc.Currency = inherit(currencyKeys[base.Provider], base.Currency)
	// Prices belong to a model: the inherited slot's prices only carry over when this slot
	// runs that same model.
	own := Price{Input: parsePrice(Get(prefix + pk.in)), Output: parsePrice(Get(prefix + pk.out)),
		CachedInput: parsePrice(Get(prefix + pk.cached))}
	if own.Set() {
		mc.Price = own
	} else if mc.Model != base.Model {
		mc.Price = Price{}
	}
	return mc
}

// PrimaryModel is the configuration of the selected (primary) provider.
func PrimaryModel() (ModelConfig, error) {
	p, err := ProviderName()
	if err != nil {
		return ModelConfig{}, err
	}
	return ModelConfigFor(SlotPrimary, p), nil
}

// FallbackModel returns the fallback configuration and whether fallback is enabled.
func FallbackModel() (ModelConfig, bool) {
	provider, enabled, _ := fallbackChoice()
	return ModelConfigFor(SlotFallback, provider), enabled
}

// fallbackChoice resolves the fallback provider and switch. A configuration saved before the
// fallback slot existed has neither; it is migrated to what SimonCAD did then: the first
// complete cloud provider other than the selected one is the fallback, and it is enabled
// only when there is one. migrated reports that the choice was derived, not saved.
func fallbackChoice() (provider string, enabled, migrated bool) {
	provider = strings.ToLower(Get(keyFallbackProvider))
	if provider == "anthropic" {
		provider = ProviderClaude
	}
	switch strings.ToLower(Get(keyFallbackEnabled)) {
	case "true", "1", "on", "yes":
		enabled = true
	case "false", "0", "off", "no":
	default:
		migrated = true
	}
	valid := provider == ProviderOpenAI || provider == ProviderClaude
	if valid && !migrated {
		return provider, enabled, false
	}
	primary, _ := ProviderName()
	if valid {
		return provider, ModelConfigFor(SlotFallback, provider).Complete() == nil, true
	}
	provider = ""
	for _, p := range FallbackProviders {
		if p == primary {
			continue
		}
		if provider == "" {
			provider = p
		}
		if ModelConfigFor(SlotFallback, p).Complete() == nil {
			return p, migrated || enabled, migrated
		}
	}
	return provider, enabled && !migrated, migrated
}

// FallbackConsent is the saved authorization for remote fallback (ConsentAsk by default).
func FallbackConsent() string {
	switch v := strings.ToLower(Get(keyFallbackConsent)); v {
	case ConsentAllow, ConsentDeny:
		return v
	}
	return ConsentAsk
}

// SetFallbackConsent persists the remote fallback authorization on its own, without
// touching the rest of the settings.
func SetFallbackConsent(v string) error {
	if v != ConsentAsk && v != ConsentAllow && v != ConsentDeny {
		return fmt.Errorf("unknown fallback consent %q", v)
	}
	return persist(map[string]string{keyFallbackConsent: v})
}

// PriceForModel is the price of the model that served a request, with its currency. A model
// is priced by the slot that configures it (primary first); a model no slot names (an older
// recording, a model alias) falls back to its provider's primary price. Local models are
// never priced.
func PriceForModel(provider, model string) (Price, string, bool) {
	if Local(provider) {
		return Price{}, "", false
	}
	if _, ok := envKeys[provider]; !ok {
		return Price{}, "", false
	}
	primary := ModelConfigFor(SlotPrimary, provider)
	for _, mc := range []ModelConfig{primary, ModelConfigFor(SlotFallback, provider), ModelConfigFor(SlotAdvanced, provider)} {
		if mc.Model == model {
			return mc.Price, mc.Currency, mc.Price.Set()
		}
	}
	return primary.Price, primary.Currency, primary.Price.Set()
}
