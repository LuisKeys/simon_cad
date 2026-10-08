package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeOllama serves /api/show (capabilities, context length) and /api/chat, recording the
// chat requests. promptEval is what the server reports having read (0 = the whole prompt).
type fakeOllama struct {
	caps       []string
	maxCtx     int
	reply      string
	promptEval int
	chats      []map[string]any
}

func (f *fakeOllama) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			if f.caps == nil {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"capabilities": f.caps,
				"model_info": map[string]any{"qwen35.context_length": f.maxCtx}})
		case "/api/chat":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.chats = append(f.chats, body)
			eval := f.promptEval
			if eval == 0 {
				eval = 100_000
			}
			w.Write([]byte(strings.Replace(f.reply, "EVAL", jsonInt(eval), 1)))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

const textReply = `{"message":{"role":"assistant","content":"ok"},"done_reason":"stop","prompt_eval_count":EVAL,"eval_count":7}`

// The native API carries the context window, the tools, the images and the tool loop.
func TestOllamaNativeRequest(t *testing.T) {
	f := &fakeOllama{caps: []string{"completion", "tools", "vision", "thinking"}, maxCtx: 262144,
		reply: `{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_x","function":{"name":"create_sphere","arguments":{"radius":50}}}]},"done_reason":"stop","prompt_eval_count":EVAL,"eval_count":9}`}
	srv := f.serve(t)
	o := &OpenAI{Flavor: "ollama", BaseURL: srv.URL, Model: "qwen3.5:9b", HTTP: srv.Client(), NumCtx: 32768}
	resp, err := o.Complete(context.Background(), Request{System: "sys", NoThinking: true,
		Tools: []ToolDef{{Name: "create_sphere", Description: "d", InputSchema: map[string]any{"type": "object"}}},
		Messages: []Message{
			{Role: "user", Content: []Block{{Type: "image", Source: &ImageSource{MediaType: "image/png", Data: "QUJD"}}, {Type: "text", Text: "look"}}},
			{Role: "assistant", Content: []Block{{Type: "tool_use", ID: "c1", Name: "get_model_summary"}}},
			{Role: "user", Content: []Block{{Type: "tool_result", ToolUseID: "c1", Content: `{"ok":true}`}}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if uses := resp.ToolUses(); len(uses) != 1 || uses[0].ID != "call_x" || string(uses[0].Input) != `{"radius":50}` ||
		resp.Usage.InputTokens != 100_000 || resp.Usage.OutputTokens != 9 {
		t.Fatalf("response %+v", resp)
	}
	got := f.chats[0]
	if opts := got["options"].(map[string]any); opts["num_ctx"] != float64(32768) {
		t.Fatalf("options %v", opts)
	}
	if got["think"] != false || got["stream"] != false || len(got["tools"].([]any)) != 1 {
		t.Fatalf("body %v", got)
	}
	msgs := got["messages"].([]any)
	user, call, result := msgs[1].(map[string]any), msgs[2].(map[string]any), msgs[3].(map[string]any)
	if user["content"] != "look" || user["images"].([]any)[0] != "QUJD" {
		t.Fatalf("user %v", user)
	}
	if fn := call["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any); fn["name"] != "get_model_summary" || fn["arguments"] == nil {
		t.Fatalf("tool call %v", call)
	}
	if result["role"] != "tool" || result["tool_name"] != "get_model_summary" {
		t.Fatalf("tool result %v", result)
	}
}

// The window asked for never exceeds the model's own maximum, 0 leaves the server's
// default, and a model without the thinking capability is not sent the switch.
func TestOllamaWindowAndThinking(t *testing.T) {
	f := &fakeOllama{caps: []string{"completion", "tools"}, maxCtx: 8192, reply: textReply}
	srv := f.serve(t)
	o := &OpenAI{Flavor: "ollama", BaseURL: srv.URL, Model: "small", HTTP: srv.Client(), NumCtx: 32768}
	if _, err := o.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}); err != nil {
		t.Fatal(err)
	}
	if f.chats[0]["options"].(map[string]any)["num_ctx"] != float64(8192) {
		t.Fatalf("window not capped: %v", f.chats[0]["options"])
	}
	if _, ok := f.chats[0]["think"]; ok {
		t.Fatal("think sent to a model that cannot think")
	}
	o.NumCtx = 0
	o.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if _, ok := f.chats[1]["options"].(map[string]any)["num_ctx"]; ok {
		t.Fatal("0 must leave the server's window")
	}
}

// A prompt the model only partly read is an error, never an answer — and one that cannot
// fit is not sent at all.
func TestOllamaTruncationIsReported(t *testing.T) {
	f := &fakeOllama{caps: []string{"completion", "tools"}, maxCtx: 262144, reply: textReply, promptEval: 2050}
	srv := f.serve(t)
	o := &OpenAI{Flavor: "ollama", BaseURL: srv.URL, Model: "qwen3.5:9b", HTTP: srv.Client(), NumCtx: 4096}
	big := Request{System: strings.Repeat("tool definitions ", 4000)} // ~68k characters
	big.Messages = []Message{{Role: "user", Content: []Block{{Type: "text", Text: "crear una esfera"}}}}
	_, err := o.Complete(context.Background(), big)
	var ce *ContextExceededError
	if !errors.Is(err, ErrContextExceeded) || !errors.As(err, &ce) || ce.Read != 0 || ce.Max != 262144 || len(f.chats) != 0 {
		t.Fatalf("too big to fit must not be sent: %v (chats %d)", err, len(f.chats))
	}
	o.NumCtx = 0 // the server's window: only the answer can tell
	_, err = o.Complete(context.Background(), big)
	if !errors.As(err, &ce) || ce.Read != 2050 || !strings.Contains(err.Error(), "only read 2050") || !strings.Contains(err.Error(), "OLLAMA_NUM_CTX") {
		t.Fatalf("truncation: %v", err)
	}
	f.promptEval = 0 // read in full
	if _, err := o.Complete(context.Background(), big); err != nil {
		t.Fatalf("a prompt read in full: %v", err)
	}
}

// Capabilities are the server's: a multimodal model reads images whatever its name, and
// OLLAMA_VISION still overrides; with no answer the name-based registry applies.
func TestOllamaCapabilitiesComeFromTheServer(t *testing.T) {
	t.Setenv("OLLAMA_VISION", "")
	srv := (&fakeOllama{caps: []string{"completion", "tools", "vision"}}).serve(t)
	o := &OpenAI{Flavor: "ollama", BaseURL: srv.URL, Model: "qwen3.5:9b", HTTP: srv.Client()}
	if c := o.Capabilities(); !c.Vision || !c.ToolCalls || c.Attachments {
		t.Fatalf("server capabilities: %+v", c)
	}
	t.Setenv("OLLAMA_VISION", "off")
	if o.Capabilities().Vision {
		t.Fatal("OLLAMA_VISION=off must win")
	}
	t.Setenv("OLLAMA_VISION", "")
	srv2 := (&fakeOllama{}).serve(t) // /api/show unavailable
	if (&OpenAI{Flavor: "ollama", BaseURL: srv2.URL, Model: "qwen3.5:9b", HTTP: srv2.Client()}).Capabilities().Vision ||
		!(&OpenAI{Flavor: "ollama", BaseURL: srv2.URL, Model: "llava:7b", HTTP: srv2.Client()}).Capabilities().Vision {
		t.Fatal("without the server, the registry decides by name")
	}
}

// A request the local model cannot hold goes to the fallback, as an unavailable one does.
func TestContextExceededFailsOver(t *testing.T) {
	local := &OpenAI{Flavor: "ollama", BaseURL: "http://127.0.0.1:1", Model: "m"}
	if !recoverable(&ContextExceededError{Model: local.Name()}) || recoverable(errors.New("bad request")) {
		t.Fatal("recoverable")
	}
}
