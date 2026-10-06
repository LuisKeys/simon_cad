// Package runtime is the single Simon CAD Agent runtime. Capabilities (geometry first)
// plug in a system prompt, a tool subset and a context builder; the runtime owns the
// provider, the tool-call loop and the recording/eval machinery.
package runtime

import (
	"context"
	"encoding/json"
)

// Block is one content block of a message (provider-neutral, Messages-API shaped).
type Block struct {
	Type      string          `json:"type"` // text | tool_use | tool_result
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

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
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
