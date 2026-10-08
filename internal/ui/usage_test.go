package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/config"
)

// metered answers in text and reports a fixed usage per call, like a live provider.
type metered struct {
	provider string
	model    string
	in, out  int
	calls    int
}

func (m *metered) Name() string { return m.provider + ":" + m.model }

func (m *metered) Complete(context.Context, runtime.Request) (*runtime.Response, error) {
	m.calls++
	r := &runtime.Response{StopReason: "end_turn", Content: []runtime.Block{{Type: "text", Text: "Done."}}}
	r.Usage.Add(runtime.Usage{InputTokens: m.in, OutputTokens: m.out,
		ByModel: map[string]runtime.ModelUsage{runtime.ModelKey(m.provider, m.model): {
			Provider: m.provider, Model: m.model, InputTokens: m.in, OutputTokens: m.out, Calls: 1}}})
	return r, nil
}

func (m *metered) Capabilities() runtime.ProviderCapabilities {
	return runtime.ProviderCapabilities{Text: true, Vision: true, Attachments: true, StructuredOutput: true, ToolCalls: true}
}

func usageOf(t *testing.T, s *Service) UsageView {
	t.Helper()
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	return st.Usage
}

func TestTurnAndSessionTokensAreAccumulated(t *testing.T) {
	noPrices(t)
	s := newSvc(t)
	s.Autonomous = false
	p := &metered{provider: config.ProviderClaude, model: "sonnet", in: 1200, out: 300}
	s.NewProvider = func() (runtime.Provider, error) { return p, nil }

	if err := s.Prompt("Make a plate", true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	u := usageOf(t, s)
	if u.Turn.InputTokens != 1200 || u.Turn.OutputTokens != 300 || u.Turn.TotalTokens != 1500 {
		t.Fatalf("turn usage: %+v", u.Turn)
	}
	if u.Session.TotalTokens != 1500 || u.Turns != 1 {
		t.Fatalf("session usage: %+v turns=%d", u.Session, u.Turns)
	}
	if len(u.Turn.Models) != 1 || u.Turn.Models[0].Model != "sonnet" || u.Turn.Models[0].Calls != 1 {
		t.Fatalf("per-model breakdown: %+v", u.Turn.Models)
	}

	// A second turn replaces the turn figure and adds to the session total.
	if err := s.Prompt("And a hole", true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	u = usageOf(t, s)
	if u.Turn.TotalTokens != 1500 || u.Session.TotalTokens != 3000 || u.Turns != 2 {
		t.Fatalf("after two turns: turn=%+v session=%+v turns=%d", u.Turn, u.Session, u.Turns)
	}
}

func TestUsageIsReportedInTheConversation(t *testing.T) {
	noPrices(t)
	s := newSvc(t)
	s.Autonomous = false
	s.NewProvider = func() (runtime.Provider, error) {
		return &metered{provider: config.ProviderClaude, model: "sonnet", in: 10, out: 2}, nil
	}
	if err := s.Prompt("Hello", true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	var note string
	for _, m := range st.Chat {
		if m.Role == "usage" {
			note = m.Text
		}
	}
	if !strings.Contains(note, "12 tokens (10 in / 2 out)") {
		t.Fatalf("usage note = %q", note)
	}
	if !strings.Contains(note, "no prices configured") {
		t.Fatalf("a cost was reported without prices: %q", note)
	}
}

func TestUsageCarriesACostEstimateWhenPricesAreConfigured(t *testing.T) {
	noPrices(t)
	t.Setenv("ANTHROPIC_INPUT_PRICE", "3")
	t.Setenv("ANTHROPIC_OUTPUT_PRICE", "15")
	s := newSvc(t)
	s.Autonomous = false
	s.NewProvider = func() (runtime.Provider, error) {
		return &metered{provider: config.ProviderClaude, model: "sonnet", in: 1_000_000, out: 100_000}, nil
	}
	if err := s.Prompt("Make a plate", true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	u := usageOf(t, s)
	if !u.Turn.Cost.Known || u.Turn.Cost.Amount != 4.5 || u.Turn.Cost.Currency != "USD" {
		t.Fatalf("turn cost: %+v", u.Turn.Cost)
	}
	if !strings.Contains(u.Session.Text, "est. USD 4.5000") {
		t.Fatalf("session text = %q", u.Session.Text)
	}
}

func TestNoUsageNoteWhenTheProviderReportsNothing(t *testing.T) {
	noPrices(t)
	s := newSvc(t)
	s.Autonomous = false
	s.NewProvider = scripted(t) // the Replay provider reports no tokens
	if err := s.Prompt("Hello", true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range st.Chat {
		if m.Role == "usage" {
			t.Fatalf("a usage note was written for an unmetered provider: %q", m.Text)
		}
	}
	if u := st.Usage; u.Turns != 0 || u.Session.TotalTokens != 0 {
		t.Fatalf("session usage: %+v", u)
	}
}

// noPrices clears the price slots so a test starts from "tokens only".
func noPrices(t *testing.T) {
	t.Helper()
	if err := config.LoadFrom(""); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ANTHROPIC_INPUT_PRICE", "ANTHROPIC_OUTPUT_PRICE", "OPENAI_INPUT_PRICE",
		"OPENAI_OUTPUT_PRICE", "OLLAMA_INPUT_PRICE", "OLLAMA_OUTPUT_PRICE", "SIMONCAD_CURRENCY"} {
		t.Setenv(k, "")
	}
}
