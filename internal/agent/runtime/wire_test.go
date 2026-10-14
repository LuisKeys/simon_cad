package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWithMessageCacheMarksLastBlockWithoutMutating(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: []Block{{Type: "text", Text: "first"}}},
		{Role: "user", Content: []Block{{Type: "tool_result", ToolUseID: "a", Content: "r1"}, {Type: "tool_result", ToolUseID: "b", Content: "r2"}}},
	}
	out := withMessageCache(msgs)
	if out[1].Content[1].CacheControl == nil || out[1].Content[0].CacheControl != nil || out[0].Content[0].CacheControl != nil {
		t.Fatalf("only the last block of the last message may carry the breakpoint: %+v", out)
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				t.Fatalf("the stored transcript was mutated: %+v", msgs)
			}
		}
	}
}

func TestWithMessageCacheSkipsThinkingAndEmptyText(t *testing.T) {
	msgs := []Message{{Role: "assistant", Content: []Block{{Type: "text", Text: "x"}, {Type: "thinking", Thinking: "t"}, {Type: "text"}}}}
	out := withMessageCache(msgs)
	if out[0].Content[0].CacheControl == nil || out[0].Content[1].CacheControl != nil || out[0].Content[2].CacheControl != nil {
		t.Fatalf("breakpoint must land on the last cacheable block: %+v", out[0].Content)
	}
	if got := withMessageCache(nil); got != nil {
		t.Fatalf("nil in, nil out: %v", got)
	}
}

func TestClaudeRequestCachesTranscriptAndForcesTool(t *testing.T) {
	type wire struct {
		Thinking   map[string]any `json:"thinking"`
		ToolChoice map[string]any `json:"tool_choice"`
		Messages   []struct {
			Content []struct {
				CacheControl map[string]any `json:"cache_control"`
			} `json:"content"`
		} `json:"messages"`
	}
	var got wire
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = wire{} // Decode leaves absent fields alone, so start clean for every request
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	}))
	defer srv.Close()
	c := &Claude{APIKey: "k", BaseURL: srv.URL, Model: "sonnet", HTTP: srv.Client()}
	req := Request{Tools: []ToolDef{{Name: "submit_plan", InputSchema: map[string]any{}}},
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got.Thinking == nil || got.ToolChoice != nil {
		t.Fatalf("without ForceTool thinking stays on and no tool is forced: %+v", got)
	}
	if cc := got.Messages[0].Content[0].CacheControl; cc["type"] != "ephemeral" {
		t.Fatalf("last message block not cached: %+v", got.Messages)
	}
	req.ForceTool = "submit_plan"
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got.ToolChoice["type"] != "tool" || got.ToolChoice["name"] != "submit_plan" || got.Thinking != nil {
		t.Fatalf("a forced call must name the tool and drop thinking: %+v", got)
	}
	req.ForceTool = "not_offered"
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got.ToolChoice != nil {
		t.Fatalf("a tool that was not offered must not be forced: %+v", got.ToolChoice)
	}
}

func TestOpenAIForcesTool(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"status":"completed","output":[],"usage":{}}`))
	}))
	defer srv.Close()
	o := &OpenAI{Flavor: "openai", APIKey: "k", BaseURL: srv.URL, Model: "gpt-x", HTTP: srv.Client()}
	_, err := o.Complete(context.Background(), Request{ForceTool: "submit_plan",
		Tools:    []ToolDef{{Name: "submit_plan", InputSchema: map[string]any{}}},
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	tc, _ := got["tool_choice"].(map[string]any)
	if tc["type"] != "function" || tc["name"] != "submit_plan" {
		t.Fatalf("tool_choice = %+v", got["tool_choice"])
	}
}

func TestRetryDelayBacksOffWithJitterAndRetryAfter(t *testing.T) {
	defer func(f func() float64) { jitter = f }(jitter)
	jitter = func() float64 { return 0 }
	if d := retryDelay(1, 0); d != time.Second {
		t.Fatalf("first retry = %v, want 1s", d)
	}
	if d := retryDelay(2, 0); d != 2*time.Second {
		t.Fatalf("second retry = %v, want 2s", d)
	}
	jitter = func() float64 { return 1 }
	if d := retryDelay(1, 0); d != 1500*time.Millisecond {
		t.Fatalf("full jitter = %v, want 1.5s", d)
	}
	if d := retryDelay(1, 7*time.Second); d != 7*time.Second {
		t.Fatalf("retry-after must win when longer: %v", d)
	}
	if d := retryDelay(1, time.Hour); d != maxRetryWait {
		t.Fatalf("retry-after must be capped: %v", d)
	}
	h := http.Header{}
	h.Set("retry-after", "3")
	if retryAfter(h) != 3*time.Second || retryAfter(http.Header{}) != 0 {
		t.Fatal("retry-after header not parsed")
	}
	h.Set("retry-after", "Wed, 21 Oct 2026 07:28:00 GMT")
	if retryAfter(h) != 0 {
		t.Fatal("the HTTP-date form is ignored")
	}
}

func TestWaitRetryStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRetry(ctx, 0, 0); err != nil {
		t.Fatalf("the first attempt never waits: %v", err)
	}
	if err := waitRetry(ctx, 1, 0); err == nil {
		t.Fatal("a cancelled context must end the wait")
	}
}

func TestClaudeHonoursRetryAfter(t *testing.T) {
	defer func(f func() float64) { jitter = f }(jitter)
	jitter = func() float64 { return 0 }
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("retry-after", "1")
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	}))
	defer srv.Close()
	c := &Claude{APIKey: "k", BaseURL: srv.URL, Model: "sonnet", HTTP: srv.Client()}
	start := time.Now()
	resp, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err != nil || !strings.Contains(resp.Text(), "ok") || calls != 2 {
		t.Fatalf("resp=%v err=%v calls=%d", resp, err, calls)
	}
	if time.Since(start) < time.Second {
		t.Fatalf("retried after %v, before the server's retry-after", time.Since(start))
	}
}

func TestTruncatedStopReasons(t *testing.T) {
	for _, r := range []string{"max_tokens", "max_output_tokens", "length"} {
		if !truncated(r) {
			t.Errorf("%q must count as truncated", r)
		}
	}
	for _, r := range []string{"", "end_turn", "tool_use", "stop", "completed"} {
		if truncated(r) {
			t.Errorf("%q must not count as truncated", r)
		}
	}
}
