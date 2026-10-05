package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAI serves OpenAI (Responses API, so reasoning models can use function tools) and
// Ollama (Chat Completions under /v1), selected by BaseURL and Flavor.
type OpenAI struct {
	Flavor  string // "openai" or "ollama"
	APIKey  string // optional for ollama
	BaseURL string
	Model   string
	HTTP    *http.Client
	// ReasoningEffort (none|low|medium|high...) is sent on the OpenAI Responses API;
	// empty leaves the model default.
	ReasoningEffort string
}

func (o *OpenAI) Name() string { return o.Flavor + ":" + o.Model }

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

func strp(s string) *string { return &s }

// toOpenAIMessages converts provider-neutral messages: tool_result blocks become role=tool
// messages and tool_use blocks become assistant tool_calls.
func toOpenAIMessages(system string, msgs []Message) []oaMessage {
	var out []oaMessage
	if system != "" {
		out = append(out, oaMessage{Role: "system", Content: strp(system)})
	}
	for _, m := range msgs {
		var texts []string
		var calls []oaToolCall
		var results []oaMessage
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				texts = append(texts, b.Text)
			case "tool_use":
				var c oaToolCall
				c.ID, c.Type = b.ID, "function"
				c.Function.Name = b.Name
				c.Function.Arguments = string(b.Input)
				if c.Function.Arguments == "" {
					c.Function.Arguments = "{}"
				}
				calls = append(calls, c)
			case "tool_result":
				results = append(results, oaMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: strp(b.Content)})
			}
		}
		out = append(out, results...)
		if len(texts) > 0 || len(calls) > 0 {
			msg := oaMessage{Role: m.Role, ToolCalls: calls}
			if len(texts) > 0 {
				msg.Content = strp(strings.Join(texts, "\n"))
			} else if m.Role == "user" {
				msg.Content = strp("")
			}
			out = append(out, msg)
		}
	}
	return out
}

func (o *OpenAI) Complete(ctx context.Context, req Request) (*Response, error) {
	model := req.Model
	if model == "" {
		model = o.Model
	}
	max := req.MaxTokens
	if max == 0 {
		max = 4096
	}
	if o.Flavor == "openai" {
		return o.completeResponses(ctx, model, max, req)
	}
	body := map[string]any{"model": model, "messages": toOpenAIMessages(req.System, req.Messages), "max_tokens": max}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
		}
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	data, err := o.post(ctx, "/v1/chat/completions", payload)
	if err != nil {
		return nil, err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   *string      `json:"content"`
				ToolCalls []oaToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return nil, fmt.Errorf("decode %s response: %v", o.Flavor, err)
	}
	ch := out.Choices[0]
	r := &Response{StopReason: ch.FinishReason, Usage: Usage{InputTokens: out.Usage.Prompt, OutputTokens: out.Usage.Completion}}
	if ch.Message.Content != nil && *ch.Message.Content != "" {
		r.Content = append(r.Content, Block{Type: "text", Text: *ch.Message.Content})
	}
	for i, c := range ch.Message.ToolCalls {
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1)
		}
		args := strings.TrimSpace(c.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		r.Content = append(r.Content, Block{Type: "tool_use", ID: id, Name: c.Function.Name, Input: json.RawMessage(args)})
	}
	return r, nil
}

// post sends payload with retry on 429/5xx and returns the 200 body.
func (o *OpenAI) post(ctx context.Context, path string, payload []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("content-type", "application/json")
		if o.APIKey != "" {
			hr.Header.Set("authorization", "Bearer "+o.APIKey)
		}
		resp, err := o.HTTP.Do(hr)
		if err != nil {
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%s API %d: %s", o.Flavor, resp.StatusCode, truncate(string(data), 300))
			continue
		}
		if resp.StatusCode != 200 {
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &e)
			if e.Error.Message == "" {
				e.Error.Message = truncate(string(data), 300)
			}
			return nil, fmt.Errorf("%s API %d: %s", o.Flavor, resp.StatusCode, e.Error.Message)
		}
		return data, nil
	}
	return nil, fmt.Errorf("%s API unavailable after retries: %w", o.Flavor, lastErr)
}

// completeResponses speaks the OpenAI Responses API statelessly. Reasoning items come back
// as "reasoning" blocks holding the raw item (with encrypted content) and are replayed on
// the next turn so tool loops keep their chain of thought.
func (o *OpenAI) completeResponses(ctx context.Context, model string, max int, req Request) (*Response, error) {
	var input []any
	for _, m := range req.Messages {
		var texts []string
		var items []any
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				texts = append(texts, b.Text)
			case "reasoning":
				items = append(items, json.RawMessage(b.Input))
			case "tool_use":
				args := string(b.Input)
				if args == "" {
					args = "{}"
				}
				items = append(items, map[string]any{"type": "function_call", "call_id": b.ID, "name": b.Name, "arguments": args})
			case "tool_result":
				items = append(items, map[string]any{"type": "function_call_output", "call_id": b.ToolUseID, "output": b.Content})
			}
		}
		input = append(input, items...)
		if len(texts) > 0 {
			input = append(input, map[string]any{"role": m.Role, "content": strings.Join(texts, "\n")})
		}
	}
	body := map[string]any{"model": model, "input": input, "max_output_tokens": max, "store": false,
		"include": []string{"reasoning.encrypted_content"}}
	if req.System != "" {
		body["instructions"] = req.System
	}
	if o.ReasoningEffort != "" {
		body["reasoning"] = map[string]any{"effort": o.ReasoningEffort}
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.InputSchema})
		}
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	data, err := o.post(ctx, "/v1/responses", payload)
	if err != nil {
		return nil, err
	}
	var out struct {
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode openai response: %v", err)
	}
	r := &Response{StopReason: out.Status, Usage: Usage{InputTokens: out.Usage.In, OutputTokens: out.Usage.Out}}
	if out.IncompleteDetails.Reason != "" {
		r.StopReason = out.IncompleteDetails.Reason
	}
	for i, raw := range out.Output {
		var it struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			Args    string `json:"arguments"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		switch it.Type {
		case "reasoning":
			r.Content = append(r.Content, Block{Type: "reasoning", Input: raw})
		case "message":
			for _, c := range it.Content {
				if c.Type == "output_text" && c.Text != "" {
					r.Content = append(r.Content, Block{Type: "text", Text: c.Text})
				}
			}
		case "function_call":
			id := it.CallID
			if id == "" {
				id = fmt.Sprintf("call_%d", i+1)
			}
			args := strings.TrimSpace(it.Args)
			if args == "" {
				args = "{}"
			}
			r.Content = append(r.Content, Block{Type: "tool_use", ID: id, Name: it.Name, Input: json.RawMessage(args)})
		}
	}
	return r, nil
}
