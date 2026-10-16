package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/config"
)

// tiered is a scripted provider of one tier: it names its provider and model, answers from a
// script (then with plain text) and records its requests.
type tiered struct {
	provider, name string
	script         []runtime.Response
	err            error
	reqs           []runtime.Request
}

func (m *tiered) Name() string                               { return m.provider + ":" + m.name }
func (m *tiered) Identity() (string, string)                 { return m.provider, m.name }
func (m *tiered) Capabilities() runtime.ProviderCapabilities { return allCaps }
func (m *tiered) Complete(_ context.Context, req runtime.Request) (*runtime.Response, error) {
	m.reqs = append(m.reqs, req)
	if m.err != nil {
		return nil, m.err
	}
	if len(m.script) == 0 {
		return &runtime.Response{Content: []runtime.Block{{Type: "text", Text: "done"}}}, nil
	}
	r := m.script[0]
	m.script = m.script[1:]
	return &r, nil
}

func use(id, tool, args string) runtime.Response {
	return runtime.Response{Content: []runtime.Block{{Type: "tool_use", ID: id, Name: tool, Input: json.RawMessage(args)}}}
}

const (
	goodBox = `{"dx":10,"dy":10,"dz":10}`
	badBox  = `{"dx":"wide"}` // fails the tool's schema
)

func ladder(mode, consent string) (r *runtime.Router, local, std, adv *tiered) {
	local = &tiered{provider: "ollama", name: "gemma"}
	std = &tiered{provider: "openai", name: "small"}
	adv = &tiered{provider: "openai", name: "big"}
	r = &runtime.Router{Selected: local, Fallbacks: []runtime.Provider{std}, Advanced: adv, Mode: mode, Consent: consent}
	return
}

func TestClassifyRules(t *testing.T) {
	ctx := context.Background()
	local := &tiered{provider: "ollama", name: "gemma"}
	cases := []struct {
		text string
		sig  runtime.Signals
		want runtime.Tier
	}{
		{"create a box 20 mm wide", runtime.Signals{}, runtime.TierLocal},
		{"mueve el cubo 10 mm en x y píntalo de rojo", runtime.Signals{}, runtime.TierLocal},
		{"use the frontier model to make a box", runtime.Signals{UserAsked: true}, runtime.TierAdvanced},
		{"make a box", runtime.Signals{Images: true}, runtime.TierAdvanced},
		{"make a box", runtime.Signals{Documents: true}, runtime.TierAdvanced},
		{"build the enclosure", runtime.Signals{Complex: true}, runtime.TierStandard},
		{"create a box, then loft it into a cylinder with tangent constraints", runtime.Signals{}, runtime.TierLocal}, // no classifier: stays local
	}
	for _, c := range cases {
		if got, why := runtime.Classify(ctx, nil, c.text, c.sig); got != c.want || why == "" {
			t.Errorf("%q: tier %v (%s), want %v", c.text, got, why, c.want)
		}
	}
	if len(local.reqs) != 0 {
		t.Fatal("rules decide without the model")
	}
}

func TestClassifyAsksTheLocalModelOnlyWhenTheRulesCannot(t *testing.T) {
	ctx := context.Background()
	vague := "I need something to hold my phone on the desk, but nicer looking"
	for tier, want := range map[string]runtime.Tier{"cloud_standard": runtime.TierStandard, "cloud_advanced": runtime.TierAdvanced, "local": runtime.TierLocal} {
		local := &tiered{provider: "ollama", name: "gemma", script: []runtime.Response{use("c", "classify_request", fmt.Sprintf(`{"tier":%q,"reason":"quoted: %s"}`, tier, vague))}}
		got, why := runtime.Classify(ctx, local, vague, runtime.Signals{})
		if got != want || len(local.reqs) != 1 || local.reqs[0].ForceTool != "classify_request" {
			t.Fatalf("%s: tier %v, %d calls", tier, got, len(local.reqs))
		}
		if strings.Contains(why, "phone") {
			t.Fatalf("the model's reason may quote the prompt and must not be kept: %q", why)
		}
	}
	// the classifier being down never reaches the cloud: the request starts local
	down := &tiered{provider: "ollama", name: "gemma", err: fmt.Errorf("down: %w", runtime.ErrUnavailable)}
	if got, _ := runtime.Classify(ctx, down, vague, runtime.Signals{}); got != runtime.TierLocal {
		t.Fatalf("classifier unavailable: %v", got)
	}
}

func TestLadderEscalatesOneTierAtATime(t *testing.T) {
	r, _, _, _ := ladder(config.ModeLocalFirst, config.ConsentAllow)
	esc := runtime.EscalatorOf(r.Agent())
	if esc.Tier() != runtime.TierLocal {
		t.Fatal("a plain agent starts local")
	}
	if e, err := esc.Escalate("x"); err != nil || e.From != runtime.TierLocal || e.To != runtime.TierStandard {
		t.Fatalf("local to standard: %+v %v", e, err)
	}
	if e, err := esc.Escalate("y"); err != nil || e.To != runtime.TierAdvanced || esc.Tier() != runtime.TierAdvanced {
		t.Fatalf("standard to advanced: %+v %v", e, err)
	}
	if _, err := esc.Escalate("z"); !errors.Is(err, runtime.ErrNoHigherTier) {
		t.Fatalf("the ladder has a top: %v", err)
	}
	// without a separate advanced model, standard is the top
	r.Advanced = nil
	esc = runtime.EscalatorOf(r.Agent())
	if _, err := esc.Escalate("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := esc.Escalate("y"); !errors.Is(err, runtime.ErrNoHigherTier) {
		t.Fatalf("standard and advanced are one model: %v", err)
	}
}

func TestEscalationNeedsAuthorization(t *testing.T) {
	for consent, declined := range map[string]bool{config.ConsentAsk: false, config.ConsentDeny: true} {
		r, _, std, adv := ladder(config.ModeLocalFirst, consent)
		esc := runtime.EscalatorOf(r.Agent())
		_, err := esc.Escalate("x")
		var ce *runtime.ConsentError
		if !errors.As(err, &ce) || ce.Declined != declined || esc.Tier() != runtime.TierLocal {
			t.Fatalf("%s: %v (tier %v)", consent, err, esc.Tier())
		}
		if len(std.reqs)+len(adv.reqs) != 0 {
			t.Fatal("nothing may be sent before authorization")
		}
		if _, _, err := r.AgentAt(runtime.TierAdvanced, "an image is attached", false); !errors.As(err, &ce) {
			t.Fatalf("%s: starting on the cloud needs authorization too: %v", consent, err)
		}
	}
}

func TestLocalOnlyNeverReachesTheCloud(t *testing.T) {
	local := &tiered{provider: "ollama", name: "gemma"}
	r := &runtime.Router{Selected: local, Mode: config.ModeLocalOnly, FallbackErr: runtime.ErrLocalOnly}
	if _, onCloud, err := r.AgentAt(runtime.TierAdvanced, "an image is attached", true); err != nil || onCloud {
		t.Fatalf("a local-only router keeps the request local: %v %v", onCloud, err)
	}
	if _, err := runtime.EscalatorOf(r.Agent()).Escalate("x"); !errors.Is(err, runtime.ErrLocalOnly) {
		t.Fatalf("escalating in local-only mode: %v", err)
	}
}

func TestStartTier(t *testing.T) {
	r, _, _, _ := ladder(config.ModeCloudPreferred, config.ConsentAsk)
	if r.StartTier(runtime.TierLocal) != runtime.TierStandard {
		t.Fatal("cloud preferred never starts local")
	}
	// choosing the mode is the authorization: no consent dialog
	if _, onCloud, err := r.AgentAt(runtime.TierLocal, "cloud preferred", false); err != nil || !onCloud {
		t.Fatalf("cloud preferred: %v %v", onCloud, err)
	}
	r.Fallbacks, r.Advanced = nil, nil
	if r.StartTier(runtime.TierAdvanced) != runtime.TierLocal {
		t.Fatal("without a cloud model every request starts local")
	}
}

// agentRun runs one request with the escalation wired the way the service does.
func agentRun(t *testing.T, r *runtime.Router, localRetries, standardRetries int, script ...string) (*runtime.Result, *app.App, error) {
	t.Helper()
	a := app.New("t", time.Now)
	t.Cleanup(a.Close)
	agent := r.Agent()
	rt := &runtime.Runtime{Provider: agent, Caps: caps(), Escalator: runtime.EscalatorOf(agent), LocalRetries: localRetries, StandardRetries: standardRetries}
	res, err := rt.Run(context.Background(), a, "make a box", app.PolicyCommit)
	return res, a, err
}

func TestInvalidToolCallIsRecoveredLocally(t *testing.T) {
	r, local, std, _ := ladder(config.ModeLocalFirst, config.ConsentAllow)
	local.script = []runtime.Response{use("1", "create_box", badBox), use("2", "create_box", badBox), use("3", "create_box", goodBox)}
	res, a, err := agentRun(t, r, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Graph.Nodes) != 1 || len(std.reqs) != 0 || len(res.Escalations) != 0 || res.Failures != 2 {
		t.Fatalf("two failed rounds are the local model's to recover: nodes %d, cloud calls %d, %+v", len(a.Graph.Nodes), len(std.reqs), res)
	}
	// the failed attempts reached the model as structured errors
	last := local.reqs[1].Messages[len(local.reqs[1].Messages)-1].Content[0]
	if !last.IsError || !strings.Contains(last.Content, "invalid arguments") {
		t.Fatalf("error feedback: %+v", last)
	}
	tr := runtime.BuildTrace(runtime.TraceInput{Result: res, Initial: runtime.TierLocal, Final: runtime.TierLocal, Mode: config.ModeLocalFirst})
	if !tr.ExecutionSuccess || tr.Attempts != 3 || tr.EstimatedAPICost != 0 {
		t.Fatalf("trace: %+v", tr)
	}
}

func TestRepeatedFailuresEscalateWithoutRepeatingWhatSucceeded(t *testing.T) {
	r, local, std, _ := ladder(config.ModeLocalFirst, config.ConsentAllow)
	local.script = []runtime.Response{use("1", "create_box", goodBox),
		use("2", "create_box", badBox), use("3", "create_box", badBox), use("4", "create_box", badBox)}
	res, a, err := agentRun(t, r, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Escalations) != 1 || res.Escalations[0].To != runtime.TierStandard || res.Escalations[0].Reason != "repeated_schema_validation_failure" {
		t.Fatalf("escalations: %+v", res.Escalations)
	}
	if len(local.reqs) != 4 || len(std.reqs) != 1 {
		t.Fatalf("local %d calls, standard %d", len(local.reqs), len(std.reqs))
	}
	if len(a.Graph.Nodes) != 1 {
		t.Fatalf("the box that succeeded must not be made again: %d nodes", len(a.Graph.Nodes))
	}
	// the new model is told what is already applied
	msgs := std.reqs[0].Messages
	note := msgs[len(msgs)-1].Content
	text := note[len(note)-1].Text
	if !strings.Contains(text, "[router]") || !strings.Contains(text, "must not be repeated") || !strings.Contains(text, res.Committed[0]) {
		t.Fatalf("hand-over note: %q", text)
	}
	if !std.reqs[0].NoThinking {
		t.Fatal("a hand-over carries tool calls without thinking blocks, so it runs without thinking")
	}
}

func TestStandardTierGetsItsOwnRetriesBeforeAdvanced(t *testing.T) {
	r, local, std, adv := ladder(config.ModeLocalFirst, config.ConsentAllow)
	local.script = []runtime.Response{use("1", "create_box", badBox)}
	std.script = []runtime.Response{use("2", "create_box", badBox), use("3", "create_box", badBox)}
	res, a, err := agentRun(t, r, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Escalations) != 2 || res.Escalations[1].To != runtime.TierAdvanced || len(adv.reqs) != 1 {
		t.Fatalf("local to standard to advanced: %+v, advanced calls %d", res.Escalations, len(adv.reqs))
	}
	if len(a.Graph.Nodes) != 0 {
		t.Fatal("nothing was created")
	}
}

func TestFailureOnlyAMoreCapableModelCouldFixEscalates(t *testing.T) {
	r, local, std, _ := ladder(config.ModeLocalFirst, config.ConsentAllow)
	// an object that does not exist is the user's to resolve, however often the model asks
	local.script = []runtime.Response{use("1", "delete_objects", `{"features":["nope"]}`), use("2", "delete_objects", `{"features":["nope"]}`),
		use("3", "delete_objects", `{"features":["nope"]}`), use("4", "delete_objects", `{"features":["nope"]}`)}
	res, _, err := agentRun(t, r, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(std.reqs) != 0 || len(res.Escalations) != 0 {
		t.Fatalf("missing context must not be sent to a paid model: %+v", res.Escalations)
	}
	if !res.Unresolved {
		t.Fatal("the failed call was never corrected")
	}
	tr := runtime.BuildTrace(runtime.TraceInput{Result: res})
	if tr.ExecutionSuccess || tr.Failure != "unresolved_tool_failure" {
		t.Fatalf("a valid response is not a successful operation: %+v", tr)
	}
}

func TestCloudEscalationIsRefusedWithAClearError(t *testing.T) {
	cases := map[string]struct {
		mode, consent, want string
		noCloud             bool
	}{
		"local only": {config.ModeLocalOnly, config.ConsentAllow, "Local only", true},
		"declined":   {config.ModeLocalFirst, config.ConsentDeny, "declined", false},
		"not asked":  {config.ModeLocalFirst, config.ConsentAsk, "not been authorized", false},
	}
	for name, c := range cases {
		r, local, std, adv := ladder(c.mode, c.consent)
		if c.noCloud {
			r.Fallbacks, r.Advanced, r.FallbackErr = nil, nil, runtime.ErrLocalOnly
		}
		local.script = []runtime.Response{use("1", "create_box", badBox), use("2", "create_box", badBox)}
		res, _, err := agentRun(t, r, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if res.Blocked == nil || !strings.Contains(res.Final, c.want) {
			t.Fatalf("%s: final %q, blocked %v", name, res.Final, res.Blocked)
		}
		if len(std.reqs)+len(adv.reqs) != 0 {
			t.Fatalf("%s: the cloud must not be used", name)
		}
		if tr := runtime.BuildTrace(runtime.TraceInput{Result: res}); tr.ExecutionSuccess || tr.Failure != "escalation_blocked" {
			t.Fatalf("%s: trace %+v", name, tr)
		}
	}
}

func TestLocalModelDownFailsOverAndACloudFailureIsReported(t *testing.T) {
	r, local, std, _ := ladder(config.ModeLocalFirst, config.ConsentAllow)
	local.err = fmt.Errorf("ollama down: %w", runtime.ErrUnavailable)
	if _, _, err := agentRun(t, r, 2, 1); err != nil || len(std.reqs) != 1 {
		t.Fatalf("local model unavailable: standard must serve it: %v, %d calls", err, len(std.reqs))
	}
	// the cloud failing too is the request's error, never retried elsewhere
	r, local, std, adv := ladder(config.ModeLocalFirst, config.ConsentAllow)
	local.err = fmt.Errorf("ollama down: %w", runtime.ErrUnavailable)
	std.err = errors.New("openai: 500")
	_, _, err := agentRun(t, r, 2, 1)
	if err == nil || !strings.Contains(err.Error(), "500") || len(adv.reqs) != 0 {
		t.Fatalf("cloud failure: %v", err)
	}
	tr := runtime.BuildTrace(runtime.TraceInput{Err: err})
	if tr.ExecutionSuccess || tr.Failure != "provider_error" {
		t.Fatalf("trace: %+v", tr)
	}
	// and with no authorization the unavailable local model is reported, not bypassed
	r, local, std, _ = ladder(config.ModeLocalFirst, config.ConsentAsk)
	local.err = fmt.Errorf("ollama down: %w", runtime.ErrUnavailable)
	if _, _, err := agentRun(t, r, 2, 1); err == nil || len(std.reqs) != 0 {
		t.Fatalf("unauthorized fallback: %v", err)
	}
}

func TestTraceAccountsLatencyAndCost(t *testing.T) {
	var u runtime.Usage
	u.Add(runtime.Usage{InputTokens: 100, OutputTokens: 20, ByModel: map[string]runtime.ModelUsage{
		"ollama/gemma": {Provider: "ollama", Model: "gemma", InputTokens: 100, OutputTokens: 20, Calls: 1}}})
	tr := runtime.BuildTrace(runtime.TraceInput{RequestID: "r1", Mode: config.ModeLocalFirst, Initial: runtime.TierLocal, Final: runtime.TierLocal,
		Result: &runtime.Result{Usage: u}, Elapsed: 1500 * time.Millisecond, Latency: map[runtime.Tier]time.Duration{runtime.TierLocal: time.Second}})
	if tr.EstimatedAPICost != 0 || tr.LocalTokens != 120 || tr.CloudTokens != 0 || tr.LatencyMS != 1500 || tr.TierLatencyMS["local"] != 1000 || !tr.ExecutionSuccess {
		t.Fatalf("local execution reports no API cost but measurable latency: %+v", tr)
	}
	b, _ := json.Marshal(tr)
	for _, k := range []string{`"request_id"`, `"initial_tier"`, `"final_tier"`, `"attempts"`, `"execution_success"`, `"estimated_api_cost"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("trace JSON lacks %s: %s", k, b)
		}
	}
}

// Attachments a local model cannot read (an image, a document) are refused in Local only
// mode with a message that says why, instead of going to a cloud model.
func TestLocalOnlyRefusesAttachmentsALocalModelCannotRead(t *testing.T) {
	local := &model{provider: "ollama", name: "gemma", caps: textCaps}
	r := &runtime.Router{Selected: local, Mode: config.ModeLocalOnly, FallbackErr: runtime.ErrLocalOnly}
	_, _, err := r.Route(runtime.ProviderCapabilities{Vision: true})
	if !errors.Is(err, runtime.ErrCapabilityUnavailable) || !strings.Contains(err.Error(), "Local only") {
		t.Fatalf("%v", err)
	}
}

// The router built from the configuration: Gemma is the local tier, the standard and advanced
// models come from their slots, and Local only builds no cloud provider at all.
func TestRouterFromEnvBuildsTheTiers(t *testing.T) {
	for k, v := range map[string]string{"SIMONCAD_PROVIDER": "ollama", "OLLAMA_MODEL": "gemma4:12b", "OLLAMA_VISION": "",
		"OPENAI_API_KEY": "k", "OPENAI_MODEL": "small", "SIMONCAD_FALLBACK_ENABLED": "true", "SIMONCAD_FALLBACK_PROVIDER": "openai",
		"SIMONCAD_ADVANCED_ENABLED": "true", "SIMONCAD_ADVANCED_PROVIDER": "openai", "SIMONCAD_ADVANCED_OPENAI_MODEL": "big",
		"SIMONCAD_ROUTING_MODE": "", "SIMONCAD_FALLBACK_CONSENT": "allow"} {
		t.Setenv(k, v)
	}
	r := runtime.NewRouterFromEnv()
	if r.Mode != config.ModeLocalFirst || r.Selected.Name() != "ollama:gemma4:12b" || r.Fallbacks[0].Name() != "openai:small" || r.Advanced == nil || r.Advanced.Name() != "openai:big" {
		t.Fatalf("tiers: mode %s, %v, %v, %v", r.Mode, r.Selected.Name(), r.Fallbacks, r.Advanced)
	}
	t.Setenv("SIMONCAD_ADVANCED_ENABLED", "false")
	if r = runtime.NewRouterFromEnv(); r.Advanced != nil {
		t.Fatal("a disabled advanced model is not built")
	}
	t.Setenv("SIMONCAD_ROUTING_MODE", "local_only")
	r = runtime.NewRouterFromEnv()
	if len(r.Fallbacks) != 0 || r.Advanced != nil || !errors.Is(r.FallbackErr, runtime.ErrLocalOnly) {
		t.Fatalf("local only: %+v", r)
	}
}
