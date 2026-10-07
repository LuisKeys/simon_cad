// Package runtime is the single Simon CAD Agent runtime. Capabilities (geometry first)
// plug in a system prompt, a tool subset and a context builder; the runtime owns the
// provider, the tool-call loop and the recording/eval machinery.
package runtime

import (
	"context"
	"encoding/json"
	"sort"
)

// Block is one content block of a message (provider-neutral, Messages-API shaped).
type Block struct {
	Type      string          `json:"type"` // text | image | document | tool_use | tool_result
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// Extended-thinking blocks (Claude); they must be replayed unchanged within a tool loop.
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"` // redacted_thinking payload
	// Title names a document block (its file name).
	Title string `json:"title,omitempty"`
	// Source is the payload of an image or document (PDF) block.
	Source *ImageSource `json:"source,omitempty"`
}

// ImageSource is a base64 image (the Messages-API wire shape).
type ImageSource struct {
	Type      string `json:"type"` // base64
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ImageCapable is implemented by providers that may be sent image blocks. A provider that
// does not implement it is assumed to accept them (Replay/Recorder wrappers).
type ImageCapable interface{ SupportsImages() bool }

// HasDocuments reports whether any message carries a document block.
func HasDocuments(msgs []Message) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "document" {
				return true
			}
		}
	}
	return false
}

// HasImages reports whether any message carries an image block.
func HasImages(msgs []Message) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "image" {
				return true
			}
		}
	}
	return false
}

type Message struct {
	Role    string  `json:"role"` // user | assistant
	Content []Block `json:"content"`
}

type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	// NoThinking asks for an instant answer; the zero value keeps thinking on.
	NoThinking bool
}

// Usage counts the tokens of one or more model calls. Totals are what every caller needs;
// ByModel keeps the per-model breakdown a turn needs to be costed, because one turn can
// span several models (the vision router, the planner and the CAD agent).
type Usage struct {
	// InputTokens is every input token the call was billed for, cached ones included.
	InputTokens int `json:"input_tokens"`
	// CachedInputTokens is the part of InputTokens the provider served from its prompt
	// cache. Providers bill those at a lower rate, so costing them at the full input price
	// overstates the spend; it is always a subset of InputTokens, never an extra.
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	OutputTokens      int `json:"output_tokens"`
	// ByModel is keyed "<provider>/<model>"; it is nil for usage no provider attributed.
	ByModel map[string]ModelUsage `json:"by_model,omitempty"`
}

// ModelUsage is what one model consumed.
type ModelUsage struct {
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	InputTokens       int    `json:"input_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens,omitempty"`
	OutputTokens      int    `json:"output_tokens"`
	Calls             int    `json:"calls"`
}

// FreshInputTokens is the input that was not served from the cache.
func (m ModelUsage) FreshInputTokens() int { return m.InputTokens - m.CachedInputTokens }

// ModelKey identifies a model for costing and reporting.
func ModelKey(provider, model string) string { return provider + "/" + model }

// usageFor is what a provider returns for one call: totals plus their attribution. in counts
// every input token (cached included) and cached is the part of it the cache served.
func usageFor(provider, model string, in, cached, out int) Usage {
	if cached > in {
		cached = in // a provider that reports them separately is normalized by the caller
	}
	return Usage{InputTokens: in, CachedInputTokens: cached, OutputTokens: out, ByModel: map[string]ModelUsage{
		ModelKey(provider, model): {Provider: provider, Model: model, InputTokens: in,
			CachedInputTokens: cached, OutputTokens: out, Calls: 1}}}
}

// Add accumulates another Usage, merging the per-model breakdown.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.CachedInputTokens += o.CachedInputTokens
	u.OutputTokens += o.OutputTokens
	for k, m := range o.ByModel {
		if u.ByModel == nil {
			u.ByModel = map[string]ModelUsage{}
		}
		cur, ok := u.ByModel[k]
		if !ok {
			cur = ModelUsage{Provider: m.Provider, Model: m.Model}
		}
		cur.InputTokens += m.InputTokens
		cur.CachedInputTokens += m.CachedInputTokens
		cur.OutputTokens += m.OutputTokens
		cur.Calls += m.Calls
		u.ByModel[k] = cur
	}
}

// FreshInputTokens is the input that was not served from the cache.
func (u Usage) FreshInputTokens() int { return u.InputTokens - u.CachedInputTokens }

// Tokens is the total of input and output tokens.
func (u Usage) Tokens() int { return u.InputTokens + u.OutputTokens }

// Models lists the per-model breakdown, heaviest first (ties broken by key for stability).
func (u Usage) Models() []ModelUsage {
	keys := make([]string, 0, len(u.ByModel))
	for k := range u.ByModel {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := u.ByModel[keys[i]], u.ByModel[keys[j]]
		if ta, tb := a.InputTokens+a.OutputTokens, b.InputTokens+b.OutputTokens; ta != tb {
			return ta > tb
		}
		return keys[i] < keys[j]
	})
	out := make([]ModelUsage, 0, len(keys))
	for _, k := range keys {
		out = append(out, u.ByModel[k])
	}
	return out
}

type Response struct {
	Content    []Block `json:"content"`
	StopReason string  `json:"stop_reason,omitempty"`
	Usage      Usage   `json:"usage,omitempty"`
}

// Provider abstracts the LLM backend so models are swappable per capability.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
}

func (r *Response) Text() string {
	var s string
	for _, b := range r.Content {
		if b.Type == "text" {
			if s != "" {
				s += "\n"
			}
			s += b.Text
		}
	}
	return s
}

func (r *Response) ToolUses() []Block {
	var out []Block
	for _, b := range r.Content {
		if b.Type == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}
