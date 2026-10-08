package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Ollama is served through its native API (/api/chat). The OpenAI-compatible endpoint
// ignores the context window a request asks for, and Ollama silently drops whatever does not
// fit in the window the model was loaded with (4096 tokens by default): a CAD agent turn — its
// system prompt and tool definitions alone run to ~16k tokens — then reaches the model cut in
// half, and the model answers as if it had no tools. So every request names its window
// (num_ctx), and a prompt the model only partly read is reported, never answered.

// ErrContextExceeded matches a request larger than the model's context window. It is a
// request the model cannot serve, so the router may hand it to the fallback model.
var ErrContextExceeded = errors.New("context window exceeded")

// ContextExceededError says how much of a prompt a local model actually read.
type ContextExceededError struct {
	Model     string
	Read      int // prompt tokens the model processed (0 = refused before sending)
	Estimated int // approximate size of the prompt, in tokens
	Window    int // num_ctx asked for (0 = the server's default)
	Max       int // the model's own maximum window (0 = unknown)
}

func (e *ContextExceededError) Error() string {
	window := "the Ollama server's default"
	if e.Window > 0 {
		window = fmt.Sprintf("%d tokens", e.Window)
	}
	what := fmt.Sprintf("%s only read %d of the ~%d prompt tokens: its context window (%s) is too small and Ollama dropped the rest", e.Model, e.Read, e.Estimated, window)
	if e.Read == 0 {
		what = fmt.Sprintf("the request (~%d tokens) does not fit in the context window of %s (%s)", e.Estimated, e.Model, window)
	}
	hint := "raise Context window in Settings › Primary Model (OLLAMA_NUM_CTX)"
	if e.Max > 0 {
		hint += fmt.Sprintf("; this model accepts up to %d tokens", e.Max)
	}
	return what + ", so the request was not answered (" + hint + ")"
}

func (e *ContextExceededError) Is(target error) bool { return target == ErrContextExceeded }

// ollamaInfo is what an Ollama server says about a model (/api/show).
type ollamaInfo struct {
	caps          map[string]bool // completion, tools, vision, thinking, ...
	contextLength int             // the model's maximum context window (0 = unknown)
}

type ollamaEntry struct {
	info *ollamaInfo
	at   time.Time
}

var (
	ollamaMu    sync.Mutex
	ollamaCache = map[string]ollamaEntry{}
)

// The answer is cached: capabilities are asked on every state refresh of the UI, and a server
// that is down must not slow each of them down. A miss is retried sooner than a hit.
const (
	ollamaInfoTTL     = 5 * time.Minute
	ollamaMissTTL     = 15 * time.Second
	ollamaShowTimeout = 2 * time.Second
)

// info returns what the server says about the model, or nil when it cannot say (unreachable,
// an older server, a test double).
func (o *OpenAI) info() *ollamaInfo {
	key := o.BaseURL + "\x00" + o.Model
	ollamaMu.Lock()
	e, ok := ollamaCache[key]
	ollamaMu.Unlock()
	if ok {
		ttl := ollamaInfoTTL
		if e.info == nil {
			ttl = ollamaMissTTL
		}
		if time.Since(e.at) < ttl {
			return e.info
		}
	}
	info := o.fetchInfo()
	ollamaMu.Lock()
	ollamaCache[key] = ollamaEntry{info, time.Now()}
	ollamaMu.Unlock()
	return info
}

func (o *OpenAI) fetchInfo() *ollamaInfo {
	ctx, cancel := context.WithTimeout(context.Background(), ollamaShowTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"model": o.Model})
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/show", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	hr.Header.Set("content-type", "application/json")
	client := o.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(hr)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Capabilities []string       `json:"capabilities"`
		ModelInfo    map[string]any `json:"model_info"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || len(out.Capabilities) == 0 {
		return nil
	}
	info := &ollamaInfo{caps: map[string]bool{}}
	for _, c := range out.Capabilities {
		info.caps[c] = true
	}
	for k, v := range out.ModelInfo {
		if f, ok := v.(float64); ok && strings.HasSuffix(k, ".context_length") {
			info.contextLength = int(f)
		}
	}
	return info
}

// ollamaCapabilities are the model's real capabilities as the server reports them (a
// multimodal model reads images whatever its name), else the name-based registry.
// OLLAMA_VISION still overrides vision either way.
func (o *OpenAI) ollamaCapabilities() ProviderCapabilities {
	c := ModelCapabilities(ProviderOllama, o.Model)
	info := o.info()
	if info == nil {
		return c
	}
	if _, set := ollamaVisionOverride(); !set {
		c.Vision = info.caps["vision"]
	}
	c.ToolCalls = info.caps["tools"]
	return c
}

// numCtx is the window to ask for: the configured one, never more than the model accepts.
func (o *OpenAI) numCtx() (window, max int) {
	window = o.NumCtx
	if info := o.info(); info != nil {
		max = info.contextLength
	}
	if window > 0 && max > 0 && window > max {
		window = max
	}
	return window, max
}

type olToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type olMessage struct {
	Role      string       `json:"role"`
	Content   string       `json:"content"`
	Images    []string     `json:"images,omitempty"` // base64, no data: prefix
	ToolCalls []olToolCall `json:"tool_calls,omitempty"`
	ToolName  string       `json:"tool_name,omitempty"` // role=tool: the tool that answered
}

// toOllamaMessages converts provider-neutral messages: tool_result blocks become role=tool
// messages naming their tool, and tool_use blocks become assistant tool_calls.
func toOllamaMessages(system string, msgs []Message) []olMessage {
	var out []olMessage
	if system != "" {
		out = append(out, olMessage{Role: "system", Content: system})
	}
	names := map[string]string{} // tool_use id → tool name
	for _, m := range msgs {
		var texts, images []string
		var calls []olToolCall
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				texts = append(texts, b.Text)
			case "image":
				if b.Source != nil {
					images = append(images, b.Source.Data)
				}
			case "tool_use":
				var c olToolCall
				c.ID, c.Function.Name, c.Function.Arguments = b.ID, b.Name, b.Input
				if len(bytes.TrimSpace(c.Function.Arguments)) == 0 {
					c.Function.Arguments = json.RawMessage("{}")
				}
				names[b.ID] = b.Name
				calls = append(calls, c)
			case "tool_result":
				out = append(out, olMessage{Role: "tool", ToolName: names[b.ToolUseID], Content: b.Content})
			}
		}
		if len(texts) > 0 || len(images) > 0 || len(calls) > 0 {
			out = append(out, olMessage{Role: m.Role, Content: strings.Join(texts, "\n"), Images: images, ToolCalls: calls})
		}
	}
	return out
}

// promptChars is the size of what a request asks the model to read, images excluded (they
// are not counted in characters).
func promptChars(req Request) int {
	n := len(req.System)
	for _, m := range req.Messages {
		for _, b := range m.Content {
			n += len(b.Text) + len(b.Content) + len(b.Input) + len(b.Name)
		}
	}
	for _, t := range req.Tools {
		schema, _ := json.Marshal(t.InputSchema)
		n += len(t.Name) + len(t.Description) + len(schema)
	}
	return n
}

// Characters per token: text and JSON schemas run at ~4 (a CAD turn measured 4.2). The
// lower bound decides truncation — a prompt that read fewer tokens than even that was cut —
// so an unusually dense prompt is never mistaken for a truncated one.
const (
	charsPerToken      = 4
	charsPerTokenBound = 6
)

func (o *OpenAI) completeOllama(ctx context.Context, model string, max int, req Request) (*Response, error) {
	window, modelMax := o.numCtx()
	chars := promptChars(req)
	exceeded := func(read int) error {
		return &ContextExceededError{Model: o.Name(), Read: read, Estimated: chars / charsPerToken, Window: window, Max: modelMax}
	}
	if window > 0 && chars/charsPerTokenBound > window {
		return nil, exceeded(0) // it cannot fit: do not make the model read half of it
	}
	body := map[string]any{"model": model, "messages": toOllamaMessages(req.System, req.Messages), "stream": false}
	opts := map[string]any{"num_predict": max}
	if window > 0 {
		opts["num_ctx"] = window
	}
	body["options"] = opts
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
		}
		body["tools"] = tools
	}
	// Only a thinking model takes the switch; others refuse a request that names it.
	if info := o.info(); info != nil && info.caps["thinking"] {
		body["think"] = !req.NoThinking
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	data, err := o.post(ctx, "/api/chat", payload)
	if err != nil {
		return nil, err
	}
	var out struct {
		Message struct {
			Content   string       `json:"content"`
			ToolCalls []olToolCall `json:"tool_calls"`
		} `json:"message"`
		DoneReason      string `json:"done_reason"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode ollama response: %v", err)
	}
	// Ollama reports the prompt tokens it actually processed (cached ones included).
	if out.PromptEvalCount > 0 && out.PromptEvalCount < chars/charsPerTokenBound {
		return nil, exceeded(out.PromptEvalCount)
	}
	r := &Response{StopReason: out.DoneReason, Usage: usageFor(o.Flavor, model, out.PromptEvalCount, 0, out.EvalCount)}
	if out.Message.Content != "" {
		r.Content = append(r.Content, Block{Type: "text", Text: out.Message.Content})
	}
	for i, c := range out.Message.ToolCalls {
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1)
		}
		args := bytes.TrimSpace(c.Function.Arguments)
		var s string
		if json.Unmarshal(args, &s) == nil { // some models send the arguments as a JSON string
			args = []byte(strings.TrimSpace(s))
		}
		if len(args) == 0 || string(args) == "null" {
			args = []byte("{}")
		}
		r.Content = append(r.Content, Block{Type: "tool_use", ID: id, Name: c.Function.Name, Input: json.RawMessage(args)})
	}
	return r, nil
}
