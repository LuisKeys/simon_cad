package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/accelone/simoncad/internal/config"
)

// FallbackOrder is the priority of cloud providers a configuration that predates the
// fallback slot is migrated with (see config.FallbackProviders).
var FallbackOrder = config.FallbackProviders

// Router is the model router: it picks a provider for a request by the capabilities the
// request needs. The selected (primary) provider wins whenever it can serve the request;
// otherwise the fallback model is used, when it is enabled and can.
type Router struct {
	// Selected is the user's provider (SIMONCAD_PROVIDER); nil when it is not configured.
	Selected Provider
	// SelectedErr explains why Selected is nil.
	SelectedErr error
	// Fallbacks are the providers tried when Selected cannot serve a request, in order.
	// Built from the configuration it holds at most the one fallback model.
	Fallbacks []Provider
	// FallbackErr explains why Fallbacks is empty (fallback disabled or incomplete).
	FallbackErr error
	// Consent is the user's authorization (config.ConsentAsk/Allow/Deny; "" = ask) to send a
	// request of a local Selected provider to a remote fallback.
	Consent string
	// OnRoute, if set, observes every model call made through the router's providers (see
	// Observe and Agent): which model serves it, and why when it is the fallback. It is
	// called while no caller lock is held, right before the call.
	OnRoute func(RouteEvent)
}

// RouteEvent says which model serves a model call.
type RouteEvent struct {
	Provider string `json:"provider"` // openai | claude | ollama ("" when unknown)
	Model    string `json:"model"`
	Local    bool   `json:"local"`
	Fallback bool   `json:"fallback"`
	// Requested is the primary model the request was meant for (fallback only).
	Requested string `json:"requested,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Label renders the event the way the interface names a model: "OpenAI · gpt-x".
func (e RouteEvent) Label() string {
	if e.Provider == "" {
		return e.Model
	}
	return config.ProviderLabel(e.Provider) + " · " + e.Model
}

// Label names a provider's model the way the interface does (see RouteEvent.Label).
func Label(p Provider) string {
	prov, model := Identify(p)
	return RouteEvent{Provider: prov, Model: model}.Label()
}

// CapabilityUnavailableError means no configured provider can serve a request.
type CapabilityUnavailableError struct {
	Missing  []string
	Selected string
	// Reason is why there is no fallback (disabled, incomplete); empty when unknown.
	Reason string
	// Disabled: the fallback model is switched off, so the request is refused, not rerouted.
	Disabled bool
}

func (e *CapabilityUnavailableError) Error() string {
	what := strings.Join(e.Missing, ", ")
	if e.Disabled && e.Selected != "" {
		return fmt.Sprintf("%s cannot handle %s input and the fallback model is disabled, so the request was not sent anywhere else (enable it in Settings › Fallback Model)", e.Selected, what)
	}
	hint := "configure a Fallback Model (OpenAI or Anthropic) in Settings"
	if e.Reason != "" {
		hint = e.Reason + "; " + hint
	}
	if e.Selected != "" {
		return fmt.Sprintf("%s cannot handle %s input and no fallback provider is configured (%s)", e.Selected, what, hint)
	}
	return fmt.Sprintf("no configured provider supports %s input (%s)", what, hint)
}

// ErrCapabilityUnavailable matches a *CapabilityUnavailableError with errors.Is.
var ErrCapabilityUnavailable = errors.New("capability unavailable")

func (e *CapabilityUnavailableError) Is(target error) bool { return target == ErrCapabilityUnavailable }

// ErrFallbackDisabled is the FallbackErr of a router whose fallback is switched off.
var ErrFallbackDisabled = errors.New("the fallback model is disabled")

// ConsentError means a request of the local primary model needs the remote fallback and the
// user has not authorized it (Declined: the user refused it).
type ConsentError struct {
	Selected string     // the local model
	Fallback RouteEvent // the remote model the request would go to
	Declined bool
}

func (e *ConsentError) Error() string {
	if e.Declined {
		return fmt.Sprintf("%s cannot handle this request and sending it to the remote fallback (%s) was declined; change it in Settings › Fallback Model", e.Selected, e.Fallback.Label())
	}
	return fmt.Sprintf("%s cannot handle this request locally: it needs the remote fallback %s, which has not been authorized", e.Selected, e.Fallback.Label())
}

// ErrConsentRequired matches a *ConsentError that can still be authorized.
var ErrConsentRequired = errors.New("remote fallback needs authorization")

func (e *ConsentError) Is(target error) bool { return target == ErrConsentRequired && !e.Declined }

// NewRouterFromEnv builds the router from the configuration. It never fails: an
// unconfigured selected provider is reported through SelectedErr, an unusable fallback
// through FallbackErr.
func NewRouterFromEnv() *Router {
	r := &Router{Consent: config.FallbackConsent()}
	name, err := ProviderNameFromEnv()
	if err == nil {
		r.Selected, err = NewNamedProviderFromEnv(name)
	}
	r.SelectedErr = err
	mc, enabled := config.FallbackModel()
	switch {
	case !enabled:
		r.FallbackErr = ErrFallbackDisabled
	case r.Selected != nil && sameModel(r.Selected, mc.Provider, mc.Model):
		r.FallbackErr = errors.New("the fallback model is the primary model") // a fallback to itself would loop
	default:
		if p, err := NewProviderFor(mc); err != nil {
			r.FallbackErr = fmt.Errorf("the fallback model is not configured: %w", mc.Complete())
		} else {
			r.Fallbacks = []Provider{p}
		}
	}
	return r
}

func sameModel(p Provider, provider, model string) bool {
	pp, pm := Identify(p)
	return pp == provider && pm == model
}

// Route returns the provider that should serve a request needing need, and whether it is a
// fallback (not the selected provider). A fallback that a local selected provider would need
// is only returned when the user authorized it (see ConsentError).
func (r *Router) Route(need ProviderCapabilities) (p Provider, fallback bool, err error) {
	p, fallback, err = r.route(need)
	if err == nil && fallback {
		err = r.consent(p)
	}
	if err != nil {
		return nil, false, err
	}
	return p, fallback, nil
}

func (r *Router) route(need ProviderCapabilities) (Provider, bool, error) {
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
	reason := ""
	if r.FallbackErr != nil {
		reason = r.FallbackErr.Error()
	}
	return nil, false, &CapabilityUnavailableError{Missing: missing, Selected: sel, Reason: reason,
		Disabled: errors.Is(r.FallbackErr, ErrFallbackDisabled)}
}

// consent is nil when fb may serve a request of the selected provider.
func (r *Router) consent(fb Provider) error {
	if r.Selected == nil || !IsLocal(r.Selected) || IsLocal(fb) {
		return nil // nothing leaves the machine that was not already going to
	}
	switch r.Consent {
	case config.ConsentAllow:
		return nil
	case config.ConsentDeny:
		return &ConsentError{Selected: Label(r.Selected), Fallback: r.event(fb, true, ""), Declined: true}
	}
	return &ConsentError{Selected: Label(r.Selected), Fallback: r.event(fb, true, "")}
}

// Supports reports whether some configured provider can serve need. A remote fallback that
// still has to be authorized counts: the user is asked when a request needs it.
func (r *Router) Supports(need ProviderCapabilities) bool {
	_, _, err := r.Route(need)
	return err == nil || errors.Is(err, ErrConsentRequired)
}

// event describes p serving a call.
func (r *Router) event(p Provider, fallback bool, reason string) RouteEvent {
	prov, model := Identify(p)
	ev := RouteEvent{Provider: prov, Model: model, Local: config.Local(prov), Fallback: fallback}
	if fallback {
		ev.Reason = reason
		if r.Selected != nil {
			_, ev.Requested = Identify(r.Selected)
		}
	}
	return ev
}

func (r *Router) emit(ev RouteEvent) {
	if r.OnRoute != nil {
		r.OnRoute(ev)
	}
}

// Observe wraps a provider returned by Route so that each of its calls is reported to
// OnRoute; reason says why a fallback serves the request.
func (r *Router) Observe(p Provider, fallback bool, reason string) Provider {
	return &observed{Provider: p, r: r, ev: r.event(p, fallback, reason)}
}

type observed struct {
	Provider
	r  *Router
	ev RouteEvent
}

func (o *observed) Complete(ctx context.Context, req Request) (*Response, error) {
	o.r.emit(o.ev)
	return o.Provider.Complete(ctx, req)
}

func (o *observed) Capabilities() ProviderCapabilities { return CapabilitiesOf(o.Provider) }
func (o *observed) Identity() (string, string)         { return Identify(o.Provider) }

// Agent returns the provider of the main agent for one request: the selected provider,
// which hands the rest of the request over to the fallback model when it fails with a
// recoverable error (ErrUnavailable, ErrContextExceeded). The hand-over happens at most once and never goes
// back, so a request cannot cycle between providers; a failing fallback fails the request.
// Use a new Agent per request.
func (r *Router) Agent() Provider { return &failover{r: r} }

type failover struct {
	r          *Router
	mu         sync.Mutex
	switched   Provider // the fallback, once the request moved to it
	reason     string
	noThinking bool
}

func (f *failover) current() Provider {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.switched != nil {
		return f.switched
	}
	return f.r.Selected
}

func (f *failover) Name() string { return f.current().Name() }

// Capabilities are the selected provider's: what a request may attach is decided before it
// starts, and a hand-over must not widen it.
func (f *failover) Capabilities() ProviderCapabilities { return CapabilitiesOf(f.r.Selected) }

func (f *failover) Identity() (string, string) { return Identify(f.current()) }

func (f *failover) Complete(ctx context.Context, req Request) (*Response, error) {
	f.mu.Lock()
	p, switched, reason, noThinking := f.r.Selected, f.switched != nil, f.reason, f.noThinking
	if switched {
		p = f.switched
	}
	f.mu.Unlock()
	if noThinking {
		req.NoThinking = true
	}
	f.r.emit(f.r.event(p, switched, reason))
	resp, err := p.Complete(ctx, req)
	if err == nil || switched || ctx.Err() != nil || !recoverable(err) {
		return resp, err
	}
	var fb Provider
	for _, c := range f.r.Fallbacks {
		if CapabilitiesOf(c).Satisfies(requestNeeds(req)) && !sameProvider(c, p) {
			fb = c
			break
		}
	}
	if fb == nil {
		return resp, err
	}
	if cerr := f.r.consent(fb); cerr != nil {
		// No dialog can be shown in the middle of a request: an unauthorized remote
		// fallback is reported, never used.
		return resp, fmt.Errorf("%w (the fallback %s was not used: remote fallback is not authorized)", err, f.r.event(fb, true, "").Label())
	}
	reason = fmt.Sprintf("%s failed: %v", p.Name(), err)
	// The conversation so far may carry tool calls made without thinking blocks, which a
	// thinking-enabled Claude request refuses; the rest of the request runs without thinking.
	noThinking = noThinking || hasToolUses(req.Messages)
	f.mu.Lock()
	f.switched, f.reason, f.noThinking = fb, reason, noThinking
	f.mu.Unlock()
	req.NoThinking = req.NoThinking || noThinking
	f.r.emit(f.r.event(fb, true, reason))
	return fb.Complete(ctx, req)
}

// recoverable errors are those the fallback model may resolve: the primary is unavailable,
// or the request does not fit in its context window.
func recoverable(err error) bool {
	return errors.Is(err, ErrUnavailable) || errors.Is(err, ErrContextExceeded)
}

func sameProvider(a, b Provider) bool {
	ap, am := Identify(a)
	bp, bm := Identify(b)
	return ap == bp && am == bm
}

// requestNeeds is what a request's content needs from a provider.
func requestNeeds(req Request) ProviderCapabilities {
	need := ProviderCapabilities{Text: true}
	need.Vision = HasImages(req.Messages)
	need.Attachments = HasDocuments(req.Messages)
	need.ToolCalls = len(req.Tools) > 0
	return need
}

func hasToolUses(msgs []Message) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "tool_use" {
				return true
			}
		}
	}
	return false
}
