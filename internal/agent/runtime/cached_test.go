package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func jsonServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}

// Anthropic reports cache tokens beside input_tokens, so the billed input is their sum.
func TestClaudeCacheTokensAreAddedToTheInput(t *testing.T) {
	srv := jsonServer(t, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":200,"cache_read_input_tokens":1800,"cache_creation_input_tokens":100,"output_tokens":50}}`)
	defer srv.Close()
	c := &Claude{APIKey: "k", BaseURL: srv.URL, Model: "sonnet", HTTP: srv.Client()}
	resp, err := c.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	u := resp.Usage
	if u.InputTokens != 2100 { // 200 fresh + 1800 read + 100 written
		t.Fatalf("input = %d, want 2100", u.InputTokens)
	}
	if u.CachedInputTokens != 1800 || u.FreshInputTokens() != 300 {
		t.Fatalf("cached = %d, fresh = %d", u.CachedInputTokens, u.FreshInputTokens())
	}
	if m := u.ByModel[ModelKey(ProviderClaude, "sonnet")]; m.CachedInputTokens != 1800 {
		t.Fatalf("per-model cached = %d", m.CachedInputTokens)
	}
}

// OpenAI reports cached_tokens inside input_tokens, so the total must not be inflated.
func TestOpenAIResponsesCacheTokensAreASubset(t *testing.T) {
	srv := jsonServer(t, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],
		"usage":{"input_tokens":20000,"output_tokens":100,"input_tokens_details":{"cached_tokens":18000}}}`)
	defer srv.Close()
	o := &OpenAI{Flavor: "openai", APIKey: "k", BaseURL: srv.URL, Model: "gpt-x", HTTP: srv.Client()}
	resp, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	u := resp.Usage
	if u.InputTokens != 20000 || u.CachedInputTokens != 18000 || u.FreshInputTokens() != 2000 {
		t.Fatalf("usage = %+v", u)
	}
	if u.Tokens() != 20100 {
		t.Fatalf("total = %d, want 20100 (cached must not be double counted)", u.Tokens())
	}
}

// A provider that reports no cache (or an older API shape) must still be counted normally.
func TestNoCacheFieldMeansNothingCached(t *testing.T) {
	srv := jsonServer(t, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],
		"usage":{"input_tokens":500,"output_tokens":20}}`)
	defer srv.Close()
	o := &OpenAI{Flavor: "openai", APIKey: "k", BaseURL: srv.URL, Model: "gpt-x", HTTP: srv.Client()}
	resp, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if u := resp.Usage; u.CachedInputTokens != 0 || u.FreshInputTokens() != 500 {
		t.Fatalf("usage = %+v", u)
	}
}

// The tool definitions and system prompt are resent every step, so the request must
// mark them as cache breakpoints: the last tool and the system block.
func TestClaudeRequestMarksCacheBreakpoints(t *testing.T) {
	var got struct {
		System []struct {
			Text         string         `json:"text"`
			CacheControl map[string]any `json:"cache_control"`
		} `json:"system"`
		Tools []struct {
			Name         string         `json:"name"`
			CacheControl map[string]any `json:"cache_control"`
		} `json:"tools"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	}))
	defer srv.Close()
	c := &Claude{APIKey: "k", BaseURL: srv.URL, Model: "sonnet", HTTP: srv.Client()}
	_, err := c.Complete(context.Background(), Request{
		System:   "be brief",
		Tools:    []ToolDef{{Name: "a", InputSchema: map[string]any{}}, {Name: "b", InputSchema: map[string]any{}}},
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.System) != 1 || got.System[0].Text != "be brief" || got.System[0].CacheControl["type"] != "ephemeral" {
		t.Fatalf("system = %+v, want one ephemeral-cached block", got.System)
	}
	if len(got.Tools) != 2 || got.Tools[0].CacheControl != nil || got.Tools[1].CacheControl["type"] != "ephemeral" {
		t.Fatalf("tools = %+v, want only the last tool cached", got.Tools)
	}
}
