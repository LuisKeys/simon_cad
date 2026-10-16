package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Routing modes (SIMONCAD_ROUTING_MODE): how much the cloud is used.
const (
	// ModeLocalOnly never uses a cloud provider.
	ModeLocalOnly = "local_only"
	// ModeLocalFirst prefers the local model and escalates to the cloud tiers when a request
	// needs them and the user authorized it (see FallbackConsent).
	ModeLocalFirst = "local_first"
	// ModeCloudPreferred sends requests to the configured cloud model directly.
	ModeCloudPreferred = "cloud_preferred"
	// ModeManual uses the selected provider and model, and the fallback only for what the
	// selected one cannot serve. It is how SimonCAD worked before routing existed.
	ModeManual = "manual"
)

// RoutingModes lists the modes in the order the Settings screen shows them.
var RoutingModes = []string{ModeLocalFirst, ModeLocalOnly, ModeCloudPreferred, ModeManual}

const (
	keyRoutingMode     = "SIMONCAD_ROUTING_MODE"
	keyLocalRetries    = "SIMONCAD_LOCAL_RETRIES"
	keyStandardRetries = "SIMONCAD_STANDARD_RETRIES"
	keyAdvancedEnabled = "SIMONCAD_ADVANCED_ENABLED"
	keyAdvancedProv    = "SIMONCAD_ADVANCED_PROVIDER"
)

// Default recovery attempts: how many consecutive failed attempts a tier gets before the
// request escalates to the next one.
const (
	DefaultLocalRetries    = 2
	DefaultStandardRetries = 1
	maxRetries             = 10
)

// RoutingMode is the configured mode. Unset, it is ModeLocalFirst for a local primary model
// (the cloud only when needed and authorized) and ModeManual otherwise, so a configuration
// that predates routing behaves as it always did.
func RoutingMode() string {
	switch m := strings.ToLower(Get(keyRoutingMode)); m {
	case ModeLocalOnly, ModeLocalFirst, ModeCloudPreferred, ModeManual:
		return m
	}
	if p, err := ProviderName(); err == nil && Local(p) {
		return ModeLocalFirst
	}
	return ModeManual
}

// LocalRetries is the number of consecutive failed attempts the local tier gets.
func LocalRetries() int { return retries(keyLocalRetries, DefaultLocalRetries) }

// StandardRetries is the number of consecutive failed attempts the Cloud Standard tier gets.
func StandardRetries() int { return retries(keyStandardRetries, DefaultStandardRetries) }

func retries(key string, def int) int {
	if n, err := strconv.Atoi(Get(key)); err == nil && n >= 0 && n <= maxRetries {
		return n
	}
	return def
}

// AdvancedModel returns the Cloud Advanced configuration and whether it is enabled. When it
// is not, the Standard model serves the advanced tier too.
func AdvancedModel() (ModelConfig, bool) {
	provider := strings.ToLower(Get(keyAdvancedProv))
	if provider == "anthropic" {
		provider = ProviderClaude
	}
	if provider != ProviderOpenAI && provider != ProviderClaude {
		provider, _, _ = fallbackChoice()
	}
	enabled := false
	switch strings.ToLower(Get(keyAdvancedEnabled)) {
	case "true", "1", "on", "yes":
		enabled = true
	}
	return ModelConfigFor(SlotAdvanced, provider), enabled
}

// RoutingSettings is the routing section of the Settings screen. Advanced is the Cloud
// Advanced model; the Standard model is Settings.Fallback.
type RoutingSettings struct {
	Mode            string           `json:"mode"`
	LocalRetries    int              `json:"localRetries"`
	StandardRetries int              `json:"standardRetries"`
	Advanced        FallbackSettings `json:"advanced"`
}

// Routing returns the effective routing settings.
func Routing() RoutingSettings {
	mc, enabled := AdvancedModel()
	adv := FallbackSettings{Enabled: enabled, Provider: mc.Provider, Providers: map[string]ProviderSettings{}, Consent: FallbackConsent()}
	for _, p := range FallbackProviders {
		adv.Providers[p] = providerView(ModelConfigFor(SlotAdvanced, p))
	}
	if enabled {
		if err := mc.Complete(); err != nil {
			adv.Problem = err.Error()
		}
	}
	return RoutingSettings{Mode: RoutingMode(), LocalRetries: LocalRetries(), StandardRetries: StandardRetries(), Advanced: adv}
}

// routingValues validates rs and returns the variables that store it.
func routingValues(rs RoutingSettings) (map[string]string, error) {
	next := map[string]string{}
	mode := strings.ToLower(rs.Mode)
	valid := false
	for _, m := range RoutingModes {
		valid = valid || m == mode
	}
	if !valid {
		return nil, fmt.Errorf("unknown routing mode %q (use one of: %s)", rs.Mode, strings.Join(RoutingModes, ", "))
	}
	if rs.LocalRetries < 0 || rs.LocalRetries > maxRetries || rs.StandardRetries < 0 || rs.StandardRetries > maxRetries {
		return nil, fmt.Errorf("recovery attempts must be between 0 and %d", maxRetries)
	}
	next[keyRoutingMode] = mode
	next[keyLocalRetries], next[keyStandardRetries] = strconv.Itoa(rs.LocalRetries), strconv.Itoa(rs.StandardRetries)
	adv := rs.Advanced
	if adv.Provider == "anthropic" {
		adv.Provider = ProviderClaude
	}
	if adv.Provider != ProviderOpenAI && adv.Provider != ProviderClaude {
		return nil, fmt.Errorf("the advanced model must be OpenAI or Anthropic, not %q", adv.Provider)
	}
	next[keyAdvancedEnabled], next[keyAdvancedProv] = fmt.Sprint(adv.Enabled), adv.Provider
	for _, p := range FallbackProviders {
		if ps, ok := adv.Providers[p]; ok {
			if err := slotValues(next, AdvancedPrefix, p, ps); err != nil {
				return nil, fmt.Errorf("advanced: %w", err)
			}
		}
	}
	return next, nil
}
