package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultModel is used when ANTHROPIC_MODEL is not set.
const DefaultModel = "claude-sonnet-5-5"

// Claude is the Anthropic Messages API provider (tool use, non-streaming in Phase 1).
type Claude struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

// NewClaudeFromEnv reads ANTHROPIC_API_KEY (OS-keychain storage arrives with the desktop app),
// ANTHROPIC_BASE_URL and ANTHROPIC_MODEL.
func NewClaudeFromEnv() (*Claude, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set; set it to use the live agent (recorded mode needs no key)")
	}
	c := &Claude{APIKey: key, BaseURL: "https://api.anthropic.com", Model: DefaultModel, HTTP: &http.Client{Timeout: 120 * time.Second}}
	if u := os.Getenv("ANTHROPIC_BASE_URL"); u != "" {
		c.BaseURL = strings.TrimRight(u, "/")
	}
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		c.Model = m
	}
	return c, nil
}

func (c *Claude) Name() string { return c.Model }

type apiTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type apiRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []apiTool `json:"tools,omitempty"`
}

type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Claude) Complete(ctx context.Context, req Request) (*Response, error) {
	model := req.Model
	if model == "" {
		model = c.Model
	}
	max := req.MaxTokens
	if max == 0 {
		max = 4096
	}
	body := apiRequest{Model: model, MaxTokens: max, System: req.System, Messages: req.Messages}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, apiTool(t))
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("x-api-key", c.APIKey)
		hr.Header.Set("anthropic-version", "2023-06-01")
		hr.Header.Set("content-type", "application/json")
		resp, err := c.HTTP.Do(hr)
		if err != nil {
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("anthropic API %d: %s", resp.StatusCode, truncate(string(data), 300))
			continue
		}
		if resp.StatusCode != 200 {
			var ae apiError
			_ = json.Unmarshal(data, &ae)
			return nil, fmt.Errorf("anthropic API %d (%s): %s", resp.StatusCode, ae.Error.Type, ae.Error.Message)
		}
		var out struct {
			Content    []Block `json:"content"`
			StopReason string  `json:"stop_reason"`
			Usage      Usage   `json:"usage"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("decode anthropic response: %w", err)
		}
		return &Response{Content: out.Content, StopReason: out.StopReason, Usage: out.Usage}, nil
	}
	return nil, fmt.Errorf("anthropic API unavailable after retries: %w", lastErr)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
