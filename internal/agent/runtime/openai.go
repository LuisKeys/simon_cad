package runtime

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OpenAI serves OpenAI (Responses API, so reasoning models can use function tools) and
// Ollama (its native /api/chat, the only Ollama API that honors a context window per
// request), selected by BaseURL and Flavor.
type OpenAI struct {
	Flavor  string // "openai" or "ollama"
	APIKey  string // optional for ollama
	BaseURL string
	Model   string
	HTTP    *http.Client
	// ReasoningEffort (none|low|medium|high|xhigh|max) is sent on the OpenAI Responses API;
	// empty leaves the model default.
	ReasoningEffort string
	// NumCtx is the context window asked of Ollama (num_ctx); 0 leaves the server's default.
	// Ollama silently drops what does not fit, so it must hold the whole prompt.
	NumCtx int

	// effortSub remembers efforts a model rejected and what was used instead, so the
	// substitution costs one extra round trip per model, not one per request.
	effortMu  sync.Mutex
	effortSub map[string]string
}

// substitute returns the effort to send in place of want (want itself when none is known).
func (o *OpenAI) substitute(want string) string {
	o.effortMu.Lock()
	defer o.effortMu.Unlock()
	if alt, ok := o.effortSub[want]; ok {
		return alt
	}
	return want
}

// rememberEffort records that want is not accepted and alt is sent instead.
func (o *OpenAI) rememberEffort(want, alt string) {
	o.effortMu.Lock()
	defer o.effortMu.Unlock()
	if o.effortSub == nil {
		o.effortSub = map[string]string{}
	}
	o.effortSub[want] = alt
}

func (o *OpenAI) Name() string { return o.Flavor + ":" + o.Model }

// Identity names the provider and model (see Identify).
func (o *OpenAI) Identity() (string, string) { return o.Flavor, o.Model }

// Capabilities comes from the registry: OpenAI accepts images and PDFs; an Ollama model
// reports what the server says it can do (see ollamaCapabilities).
func (o *OpenAI) Capabilities() ProviderCapabilities {
	if o.Flavor == ProviderOllama {
		return o.ollamaCapabilities()
	}
	return ModelCapabilities(o.Flavor, o.Model)
}

// SupportsImages reports the vision capability (kept for ImageCapable callers).
func (o *OpenAI) SupportsImages() bool { return o.Capabilities().Vision }

func (o *OpenAI) Complete(ctx context.Context, req Request) (*Response, error) {
	if HasImages(req.Messages) && !o.SupportsImages() {
		return nil, fmt.Errorf("provider %s does not support image input", o.Flavor)
	}
	if HasDocuments(req.Messages) && !o.Capabilities().Attachments {
		return nil, fmt.Errorf("provider %s does not support document input", o.Flavor)
	}
	model := req.Model
	if model == "" {
		model = o.Model
	}
	max := req.MaxTokens
	if max == 0 {
		max = 4096
	}
	if o.Flavor == ProviderOllama {
		return o.completeOllama(ctx, model, max, req)
	}
	return o.completeResponses(ctx, model, max, req)
}

// post sends payload with retry on 429/5xx and returns the 200 body.
func (o *OpenAI) post(ctx context.Context, path string, payload []byte) ([]byte, error) {
	var lastErr error
	var after time.Duration
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := waitRetry(ctx, attempt, after); err != nil {
			return nil, err
		}
		after = 0
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
			after = retryAfter(resp.Header)
			lastErr = fmt.Errorf("%s API %d: %s", o.Flavor, resp.StatusCode, truncate(string(data), 300))
			continue
		}
		if resp.StatusCode != 200 {
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			var plain struct {
				Error string `json:"error"` // Ollama's native API
			}
			if json.Unmarshal(data, &e) != nil || e.Error.Message == "" {
				_ = json.Unmarshal(data, &plain)
				e.Error.Message = cmp.Or(plain.Error, truncate(string(data), 300))
			}
			return nil, fmt.Errorf("%s API %d: %s", o.Flavor, resp.StatusCode, e.Error.Message)
		}
		return data, nil
	}
	return nil, unavailable(fmt.Errorf("%s API unavailable after retries: %w", o.Flavor, lastErr))
}

// completeResponses speaks the OpenAI Responses API statelessly. Reasoning items come back
// as "reasoning" blocks holding the raw item (with encrypted content) and are replayed on
// the next turn so tool loops keep their chain of thought.
func (o *OpenAI) completeResponses(ctx context.Context, model string, max int, req Request) (*Response, error) {
	var input []any
	for _, m := range req.Messages {
		var texts []string
		var items []any
		var parts []any // input_text / input_image parts, used when the message has images
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				texts = append(texts, b.Text)
				parts = append(parts, map[string]any{"type": "input_text", "text": b.Text})
			case "image":
				if b.Source != nil {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:" + b.Source.MediaType + ";base64," + b.Source.Data})
				}
			case "document":
				if b.Source != nil {
					parts = append(parts, map[string]any{"type": "input_file", "filename": cmp.Or(b.Title, "document.pdf"),
						"file_data": "data:" + b.Source.MediaType + ";base64," + b.Source.Data})
				}
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
		if len(parts) > len(texts) { // at least one image or file: send content parts
			input = append(input, map[string]any{"role": m.Role, "content": parts})
		} else if len(texts) > 0 {
			input = append(input, map[string]any{"role": m.Role, "content": strings.Join(texts, "\n")})
		}
	}
	body := map[string]any{"model": model, "input": input, "max_output_tokens": max, "store": false,
		"include": []string{"reasoning.encrypted_content"}}
	if req.System != "" {
		body["instructions"] = req.System
	}
	// "none" asks for an instant answer; models that have no such level are sent the weakest
	// one they do accept (see the retry below) rather than failing the turn.
	want := o.ReasoningEffort
	if req.NoThinking {
		want = "none"
	}
	effort := o.substitute(want)
	if effort != "" {
		body["reasoning"] = map[string]any{"effort": effort}
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.InputSchema})
		}
		body["tools"] = tools
		if req.ForceTool != "" && hasTool(req.Tools, req.ForceTool) {
			body["tool_choice"] = map[string]any{"type": "function", "name": req.ForceTool}
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	data, err := o.post(ctx, "/v1/responses", payload)
	if err != nil && effort != "" {
		// The model refused this reasoning level and named the ones it takes: adopt the
		// closest and retry, so the choice of model does not break "Thinking mode off".
		alt, ok := altEffort(err.Error(), effort)
		if ok {
			o.rememberEffort(want, alt)
			body["reasoning"] = map[string]any{"effort": alt}
			if payload, err = json.Marshal(body); err != nil {
				return nil, err
			}
			data, err = o.post(ctx, "/v1/responses", payload)
		}
	}
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
			In       int `json:"input_tokens"`
			Out      int `json:"output_tokens"`
			InDetail struct {
				// Part of input_tokens: the prefix OpenAI served from its prompt cache.
				Cached int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode openai response: %v", err)
	}
	r := &Response{StopReason: out.Status,
		Usage: usageFor(o.Flavor, model, out.Usage.In, out.Usage.InDetail.Cached, out.Usage.Out)}
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
