package runtime

import (
	"errors"
	"fmt"
	"strings"
)

// FallbackOrder is the priority of cloud providers used when the selected provider lacks a
// capability a request needs.
var FallbackOrder = []string{ProviderOpenAI, ProviderClaude}

// Router is the model router: it picks a provider for a request by the capabilities the
// request needs. The selected provider wins whenever it can serve the request; otherwise
// the first configured fallback that can (OpenAI, then Claude) is used.
type Router struct {
	// Selected is the user's provider (SIMONCAD_PROVIDER); nil when it is not configured.
	Selected Provider
	// SelectedErr explains why Selected is nil.
	SelectedErr error
	// Fallbacks are the configured cloud providers in priority order (Selected excluded).
	Fallbacks []Provider
}

// CapabilityUnavailableError means no configured provider can serve a request.
type CapabilityUnavailableError struct {
	Missing  []string
	Selected string
}

func (e *CapabilityUnavailableError) Error() string {
	what := strings.Join(e.Missing, ", ")
	hint := "configure OPENAI_API_KEY and OPENAI_MODEL, or ANTHROPIC_API_KEY"
	if e.Selected != "" {
		return fmt.Sprintf("%s cannot handle %s input and no fallback provider is configured (%s)", e.Selected, what, hint)
	}
	return fmt.Sprintf("no configured provider supports %s input (%s)", what, hint)
}

// ErrCapabilityUnavailable matches a *CapabilityUnavailableError with errors.Is.
var ErrCapabilityUnavailable = errors.New("capability unavailable")

func (e *CapabilityUnavailableError) Is(target error) bool { return target == ErrCapabilityUnavailable }

// NewRouterFromEnv builds the router from the environment. It never fails: an unconfigured
// selected provider is reported through SelectedErr, and fallbacks are simply the cloud
// providers whose configuration is complete.
func NewRouterFromEnv() *Router {
	r := &Router{}
	name, err := ProviderNameFromEnv()
	if err == nil {
		r.Selected, err = NewNamedProviderFromEnv(name)
	}
	r.SelectedErr = err
	for _, fb := range FallbackOrder {
		if fb == name {
			continue
		}
		if p, err := NewNamedProviderFromEnv(fb); err == nil {
			r.Fallbacks = append(r.Fallbacks, p)
		}
	}
	return r
}

// Route returns the provider that should serve a request needing need, and whether it is a
// fallback (not the selected provider).
func (r *Router) Route(need ProviderCapabilities) (p Provider, fallback bool, err error) {
	if r.Selected != nil && CapabilitiesOf(r.Selected).Satisfies(need) {
		return r.Selected, false, nil
	}
	for _, fb := range r.Fallbacks {
		if CapabilitiesOf(fb).Satisfies(need) {
			return fb, true, nil
		}
	}
	missing := allCapabilities.Missing(need)
	sel := ""
	if r.Selected != nil {
		sel = r.Selected.Name()
		missing = CapabilitiesOf(r.Selected).Missing(need)
	}
	return nil, false, &CapabilityUnavailableError{Missing: missing, Selected: sel}
}

// Supports reports whether some configured provider can serve need.
func (r *Router) Supports(need ProviderCapabilities) bool {
	_, _, err := r.Route(need)
	return err == nil
}
