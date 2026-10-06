package runtime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	geo "github.com/accelone/simoncad/internal/agent/capabilities/geometry"
	vis "github.com/accelone/simoncad/internal/agent/capabilities/vision"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
)

func caps() []runtime.Capability { return []runtime.Capability{geo.New(), vis.New()} }

// Recorded evals run in CI with no network.
func TestRecordedEvals(t *testing.T) {
	cases, err := runtime.LoadCases("../../../testdata/agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 6 {
		t.Fatalf("expected the eval corpus, found %d cases", len(cases))
	}
	var suite runtime.Suite
	for _, c := range cases {
		rep := runtime.RunCase(context.Background(), c, caps(), nil)
		suite.Reports = append(suite.Reports, rep)
		if !rep.Pass {
			t.Errorf("%s FAILED:\n  %v", rep.Name, rep.Failures)
		}
	}
	t.Logf("recorded eval pass rate: %d/%d", suite.Passed(), len(suite.Reports))
}

func TestFreeFormTextNeverMutates(t *testing.T) {
	a := app.New("t", time.Now)
	defer a.Close()
	rec := runtime.Recording{Source: "scripted", Responses: []runtime.Response{
		{Content: []runtime.Block{{Type: "text", Text: "Done! I created a 100 mm cube and added holes."}}},
	}}
	rt := &runtime.Runtime{Provider: &runtime.Replay{Rec: rec}, Caps: caps()}
	res, err := rt.Run(context.Background(), a, "make a cube", app.PolicyCommit)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Graph.Nodes) != 0 || res.ToolCalls != 0 {
		t.Fatal("text must not change the model")
	}
}

func TestOnlyRegisteredToolsAreCallable(t *testing.T) {
	a := app.New("t", time.Now)
	defer a.Close()
	rec := runtime.Recording{Responses: []runtime.Response{
		{Content: []runtime.Block{{Type: "tool_use", ID: "x", Name: "run_shell", Input: json.RawMessage(`{"cmd":"rm -rf /"}`)}}},
		{Content: []runtime.Block{{Type: "text", Text: "ok"}}},
	}}
	rt := &runtime.Runtime{Provider: &runtime.Replay{Rec: rec}, Caps: caps()}
	res, err := rt.Run(context.Background(), a, "x", app.PolicyCommit)
	if err != nil {
		t.Fatal(err)
	}
	last := res.Transcript[len(res.Transcript)-2].Content[0]
	if !last.IsError || res.ToolCalls != 0 {
		t.Fatalf("unknown tool must be refused: %+v", last)
	}
}

func TestPreviewPolicyLeavesChangeSetPending(t *testing.T) {
	a := app.New("t", time.Now)
	defer a.Close()
	rec := runtime.Recording{Responses: []runtime.Response{
		{Content: []runtime.Block{{Type: "tool_use", ID: "1", Name: "create_box", Input: json.RawMessage(`{"dx":10,"dy":10,"dz":10}`)}}},
		{Content: []runtime.Block{{Type: "text", Text: "Proposed a cube; awaiting approval."}}},
	}}
	rt := &runtime.Runtime{Provider: &runtime.Replay{Rec: rec}, Caps: caps()}
	res, err := rt.Run(context.Background(), a, "cube", app.PolicyPreview)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Graph.Nodes) != 0 || len(res.Pending) != 1 || len(a.Pending()) != 1 {
		t.Fatalf("preview must leave a pending ChangeSet: %+v", res)
	}
}

func TestContextBuilderIsDeterministicAndIncludesSelection(t *testing.T) {
	build := func() string {
		a := app.New("t", func() time.Time { return time.Unix(0, 0) })
		defer a.Close()
		a.ApplyTool("create_box", []byte(`{"id":"plate","dx":40,"dy":30,"dz":4}`), app.PolicyCommit)
		if err := a.SetSelection([]string{"face(feature=plate,side=+Z)"}); err != nil {
			t.Fatal(err)
		}
		s, err := geo.New().BuildContext(a)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := build(), build()
	if a != b {
		t.Fatal("context differs between identical runs")
	}
	for _, want := range []string{`"plate"`, "face(feature=plate,side=+Z)", "Units: mm", `"area": 1200`} {
		if !contains(a, want) {
			t.Errorf("context missing %q:\n%s", want, a)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool { return indexOf(s, sub) >= 0 })()
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestClaudeProviderWireFormat(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"content":[{"type":"tool_use","id":"tu_9","name":"create_box","input":{"dx":1,"dy":2,"dz":3}}],"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":7}}`))
	}))
	defer srv.Close()
	c := &runtime.Claude{APIKey: "test-key", BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	resp, err := c.Complete(context.Background(), runtime.Request{
		System: "sys", Messages: []runtime.Message{{Role: "user", Content: []runtime.Block{{Type: "text", Text: "hi"}}}},
		Tools: []runtime.ToolDef{{Name: "create_box", Description: "d", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Get("x-api-key") != "test-key" || hdr.Get("anthropic-version") == "" {
		t.Fatalf("headers %v", hdr)
	}
	if got["model"] != "m" || got["system"] != "sys" || got["tools"] == nil || got["max_tokens"] == nil {
		t.Fatalf("request body %v", got)
	}
	if uses := resp.ToolUses(); len(uses) != 1 || uses[0].Name != "create_box" || resp.Usage.OutputTokens != 7 {
		t.Fatalf("response %+v", resp)
	}
}

func TestOpenAIResponsesWireFormatAndToolRoundTrip(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("authorization")
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"abc","summary":[]},{"type":"function_call","call_id":"call_7","name":"create_box","arguments":"{\"dx\":1,\"dy\":2,\"dz\":3}"}],"usage":{"input_tokens":5,"output_tokens":3}}`))
	}))
	defer srv.Close()
	o := &runtime.OpenAI{Flavor: "openai", APIKey: "k", BaseURL: srv.URL, Model: "m", HTTP: srv.Client(), ReasoningEffort: "medium"}
	resp, err := o.Complete(context.Background(), runtime.Request{
		System: "sys",
		Messages: []runtime.Message{
			{Role: "user", Content: []runtime.Block{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Content: []runtime.Block{
				{Type: "reasoning", Input: json.RawMessage(`{"type":"reasoning","id":"rs_0","encrypted_content":"x"}`)},
				{Type: "tool_use", ID: "c1", Name: "get_model_summary", Input: json.RawMessage(`{}`)}}},
			{Role: "user", Content: []runtime.Block{{Type: "tool_result", ToolUseID: "c1", Content: `{"ok":true}`}}},
		},
		Tools: []runtime.ToolDef{{Name: "create_box", Description: "d", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer k" || got["max_output_tokens"] == nil || got["tools"] == nil || got["instructions"] != "sys" ||
		got["reasoning"].(map[string]any)["effort"] != "medium" {
		t.Fatalf("request: auth=%q body=%v", auth, got)
	}
	kinds := []string{}
	for _, it := range got["input"].([]any) {
		m := it.(map[string]any)
		if k, ok := m["type"].(string); ok {
			kinds = append(kinds, k)
		} else {
			kinds = append(kinds, m["role"].(string))
		}
	}
	if want := "user,reasoning,function_call,function_call_output"; strings.Join(kinds, ",") != want {
		t.Fatalf("input items %v", kinds)
	}
	if uses := resp.ToolUses(); len(uses) != 1 || uses[0].ID != "call_7" || string(uses[0].Input) != `{"dx":1,"dy":2,"dz":3}` || resp.Usage.InputTokens != 5 {
		t.Fatalf("response %+v", resp)
	}
	if len(resp.Content) != 2 || resp.Content[0].Type != "reasoning" {
		t.Fatalf("reasoning block not preserved: %+v", resp.Content)
	}
}

func TestOllamaUsesChatCompletions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer srv.Close()
	o := &runtime.OpenAI{Flavor: "ollama", BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	resp, err := o.Complete(context.Background(), runtime.Request{Messages: []runtime.Message{{Role: "user", Content: []runtime.Block{{Type: "text", Text: "hi"}}}}})
	if err != nil || resp.Text() != "ok" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestProviderSelectionFromEnv(t *testing.T) {
	t.Setenv("SIMONCAD_PROVIDER", "bogus")
	if _, err := runtime.NewProviderFromEnv(); err == nil {
		t.Fatal("unknown provider must fail")
	}
	t.Setenv("SIMONCAD_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := runtime.NewProviderFromEnv(); err == nil {
		t.Fatal("missing key must fail")
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "gpt-x")
	if p, err := runtime.NewProviderFromEnv(); err != nil || p.Name() != "openai:gpt-x" {
		t.Fatalf("openai: %v %v", p, err)
	}
	t.Setenv("SIMONCAD_PROVIDER", "ollama")
	t.Setenv("OLLAMA_MODEL", "llama3.1")
	if p, err := runtime.NewProviderFromEnv(); err != nil || p.Name() != "ollama:llama3.1" {
		t.Fatalf("ollama: %v %v", p, err)
	}
	t.Setenv("SIMONCAD_PROVIDER", "claude")
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("ANTHROPIC_MODEL", "claude-x")
	if p, err := runtime.NewProviderFromEnv(); err != nil || p.Name() != "claude-x" {
		t.Fatalf("claude: %v %v", p, err)
	}
}
