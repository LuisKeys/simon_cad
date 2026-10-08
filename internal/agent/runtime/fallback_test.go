package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/config"
)

// model is a provider double that knows its provider and model and records its calls.
type model struct {
	provider, name string
	caps           runtime.ProviderCapabilities
	err            error
	calls          int
	thinking       []bool
}

func (m *model) Name() string                               { return m.provider + ":" + m.name }
func (m *model) Identity() (string, string)                 { return m.provider, m.name }
func (m *model) Capabilities() runtime.ProviderCapabilities { return m.caps }
func (m *model) Complete(_ context.Context, req runtime.Request) (*runtime.Response, error) {
	m.calls++
	m.thinking = append(m.thinking, !req.NoThinking)
	if m.err != nil {
		return nil, m.err
	}
	return &runtime.Response{Content: []runtime.Block{{Type: "text", Text: m.name}}}, nil
}

var (
	textCaps = runtime.ProviderCapabilities{Text: true, StructuredOutput: true, ToolCalls: true}
	allCaps  = runtime.ProviderCapabilities{Text: true, Vision: true, Attachments: true, StructuredOutput: true, ToolCalls: true}
)

// A local primary never sends a request to a remote fallback without authorization.
func TestRemoteFallbackNeedsConsent(t *testing.T) {
	local, remote := &model{provider: "ollama", name: "qwen3", caps: textCaps}, &model{provider: "openai", name: "gpt-x", caps: allCaps}
	vision := runtime.ProviderCapabilities{Vision: true}
	r := &runtime.Router{Selected: local, Fallbacks: []runtime.Provider{remote}}

	_, _, err := r.Route(vision)
	var ce *runtime.ConsentError
	if !errors.Is(err, runtime.ErrConsentRequired) || !errors.As(err, &ce) || ce.Fallback.Label() != "OpenAI · gpt-x" {
		t.Fatalf("ask: %v", err)
	}
	if !r.Supports(vision) {
		t.Fatal("a fallback that can still be authorized is supported")
	}
	if p, _, err := r.Route(runtime.ProviderCapabilities{Text: true}); err != nil || p != local {
		t.Fatalf("compatible requests stay local without asking: %v %v", p, err)
	}

	r.Consent = config.ConsentDeny
	if _, _, err := r.Route(vision); err == nil || errors.Is(err, runtime.ErrConsentRequired) || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("deny: %v", err)
	}
	if r.Supports(vision) {
		t.Fatal("a declined fallback does not support anything")
	}

	r.Consent = config.ConsentAllow
	if p, fb, err := r.Route(vision); err != nil || !fb || p != remote {
		t.Fatalf("allow: %v %v %v", p, fb, err)
	}

	// cloud → cloud needs no consent: nothing leaves the machine that was not already leaving
	r = &runtime.Router{Selected: &model{provider: "claude", name: "x", caps: textCaps}, Fallbacks: []runtime.Provider{remote}}
	if _, fb, err := r.Route(vision); err != nil || !fb {
		t.Fatalf("cloud primary: %v", err)
	}
}

// The agent provider hands a request over to the fallback once, on a recoverable error, and
// never goes back; each call is reported to OnRoute.
func TestAgentFailsOverOnce(t *testing.T) {
	down := fmt.Errorf("ollama API unavailable after retries: %w", runtime.ErrUnavailable)
	local, remote := &model{provider: "ollama", name: "qwen3", caps: textCaps, err: down}, &model{provider: "openai", name: "gpt-x", caps: allCaps}
	var events []runtime.RouteEvent
	r := &runtime.Router{Selected: local, Fallbacks: []runtime.Provider{remote}, Consent: config.ConsentAllow,
		OnRoute: func(ev runtime.RouteEvent) { events = append(events, ev) }}
	agent := r.Agent()
	if runtime.CapabilitiesOf(agent).Vision {
		t.Fatal("the agent reports the primary's capabilities")
	}
	tools := []runtime.Message{{Role: "assistant", Content: []runtime.Block{{Type: "tool_use", ID: "1", Name: "x"}}}}
	resp, err := agent.Complete(context.Background(), runtime.Request{Messages: tools})
	if err != nil || resp.Text() != "gpt-x" || local.calls != 1 || remote.calls != 1 {
		t.Fatalf("failover: %v %v local=%d remote=%d", resp, err, local.calls, remote.calls)
	}
	if remote.thinking[0] {
		t.Fatal("a hand-over mid tool loop runs without thinking")
	}
	if len(events) != 2 || events[0].Fallback || !events[0].Local || !events[1].Fallback || events[1].Requested != "qwen3" ||
		!strings.Contains(events[1].Reason, "unavailable") {
		t.Fatalf("events: %+v", events)
	}
	// the rest of the request stays on the fallback, and a failing fallback fails the request
	local.err = nil
	remote.err = fmt.Errorf("openai API unavailable after retries: %w", runtime.ErrUnavailable)
	if _, err := agent.Complete(context.Background(), runtime.Request{}); err == nil || local.calls != 1 || remote.calls != 2 {
		t.Fatalf("no cycle back to the primary: %v local=%d remote=%d", err, local.calls, remote.calls)
	}
}

func TestAgentDoesNotFailOverWithoutCause(t *testing.T) {
	remote := &model{provider: "openai", name: "gpt-x", caps: allCaps}
	// a non-recoverable error (bad request, auth) is the primary's to report
	local := &model{provider: "ollama", name: "qwen3", caps: textCaps, err: errors.New("ollama API 400: bad request")}
	r := &runtime.Router{Selected: local, Fallbacks: []runtime.Provider{remote}, Consent: config.ConsentAllow}
	if _, err := r.Agent().Complete(context.Background(), runtime.Request{}); err == nil || remote.calls != 0 {
		t.Fatalf("bad request: %v remote=%d", err, remote.calls)
	}
	// an unauthorized remote fallback is reported, not used
	local.err = runtime.ErrUnavailable
	r.Consent = config.ConsentAsk
	if _, err := r.Agent().Complete(context.Background(), runtime.Request{}); err == nil || remote.calls != 0 || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("unauthorized: %v remote=%d", err, remote.calls)
	}
	// no fallback configured (disabled): the primary's error stands
	r = &runtime.Router{Selected: local, FallbackErr: runtime.ErrFallbackDisabled}
	if _, err := r.Agent().Complete(context.Background(), runtime.Request{}); !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("disabled: %v", err)
	}
}
