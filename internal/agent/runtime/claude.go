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

	"github.com/accelone/simoncad/internal/config"
)

// DefaultModel is used when ANTHROPIC_MODEL is not set.
const DefaultModel = config.DefaultClaudeModel

// Claude is the Anthropic Messages API provider (tool use, non-streaming in Phase 1).
type Claude struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

// NewClaudeFromEnv reads the primary-slot Anthropic configuration: ANTHROPIC_API_KEY
// (OS-keychain storage arrives with the desktop app), ANTHROPIC_BASE_URL and ANTHROPIC_MODEL.
func NewClaudeFromEnv() (*Claude, error) {
	return newClaude(config.ModelConfigFor(config.SlotPrimary, ProviderClaude))
}

func newClaude(mc config.ModelConfig) (*Claude, error) {
	if mc.APIKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set; set it to use the live agent (recorded mode needs no key)")
	}
	c := &Claude{APIKey: mc.APIKey, BaseURL: "https://api.anthropic.com", Model: DefaultModel, HTTP: &http.Client{Timeout: 120 * time.Second}}
	if mc.BaseURL != "" {
		c.BaseURL = strings.TrimRight(mc.BaseURL, "/")
	}
	if mc.Model != "" {
		c.Model = mc.Model
	}
	return c, nil
}

func (c *Claude) Name() string { return c.Model }

// Identity names the provider and model (see Identify).
func (c *Claude) Identity() (string, string) { return ProviderClaude, c.Model }

type apiTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []Message    `json:"messages"`
	Tools     []apiTool    `json:"tools,omitempty"`
	Thinking  *apiThinking `json:"thinking,omitempty"`
}

type apiThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// thinkingBudget is the extended-thinking token budget used when thinking is on.
const thinkingBudget = 4096

type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Claude) SupportsImages() bool { return true }

// Capabilities: Claude accepts images, PDF documents and tools.
func (c *Claude) Capabilities() ProviderCapabilities {
	return ModelCapabilities(ProviderClaude, c.Model)
}

// maxImageBytes is the Messages API limit for one image.
const maxImageBytes = 5 << 20

func checkImageSizes(msgs []Message) error {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "image" && b.Source != nil && len(b.Source.Data)*3/4 > maxImageBytes {
				return fmt.Errorf("an attached image is larger than %d MB; downscale it first", maxImageBytes>>20)
			}
		}
	}
	return nil
}

func (c *Claude) Complete(ctx context.Context, req Request) (*Response, error) {
	if err := checkImageSizes(req.Messages); err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		model = c.Model
	}
	max := req.MaxTokens
	if max == 0 {
		max = 4096
	}
	body := apiRequest{Model: model, MaxTokens: max, System: req.System, Messages: req.Messages}
	if !req.NoThinking {
		body.Thinking = &apiThinking{Type: "enabled", BudgetTokens: thinkingBudget}
		if body.MaxTokens <= thinkingBudget {
			body.MaxTokens = thinkingBudget + max
		}
	}
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
			Usage      struct {
				// Anthropic reports cache tokens *beside* input_tokens, not inside it, so the
				// billed input is their sum. Cache writes cost more than fresh input rather
				// than less; this agent never writes a cache, so they join the fresh side.
				InputTokens   int `json:"input_tokens"`
				CacheRead     int `json:"cache_read_input_tokens"`
				CacheCreation int `json:"cache_creation_input_tokens"`
				OutputTokens  int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("decode anthropic response: %w", err)
		}
		in := out.Usage.InputTokens + out.Usage.CacheRead + out.Usage.CacheCreation
		return &Response{Content: out.Content, StopReason: out.StopReason,
			Usage: usageFor(ProviderClaude, model, in, out.Usage.CacheRead, out.Usage.OutputTokens)}, nil
	}
	return nil, unavailable(fmt.Errorf("anthropic API unavailable after retries: %w", lastErr))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
