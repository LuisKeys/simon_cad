package runtime

import (
	"strings"

	"github.com/accelone/simoncad/internal/config"
)

// ProviderCapabilities is what a provider/model can accept. The router selects a provider by
// the capabilities a request needs, never by provider name, so a future local multimodal
// model works without changing the routing.
type ProviderCapabilities struct {
	Text             bool `json:"text"`
	Vision           bool `json:"vision"`      // image blocks
	Attachments      bool `json:"attachments"` // document blocks (PDF)
	StructuredOutput bool `json:"structured_output"`
	ToolCalls        bool `json:"tool_calls"`
}

// Missing lists the capabilities of need that c lacks (empty when c satisfies need).
func (c ProviderCapabilities) Missing(need ProviderCapabilities) []string {
	var out []string
	for _, x := range []struct {
		name      string
		need, has bool
	}{
		{"text", need.Text, c.Text},
		{"vision", need.Vision, c.Vision},
		{"attachments", need.Attachments, c.Attachments},
		{"structured_output", need.StructuredOutput, c.StructuredOutput},
		{"tool_calls", need.ToolCalls, c.ToolCalls},
	} {
		if x.need && !x.has {
			out = append(out, x.name)
		}
	}
	return out
}

// Satisfies reports whether c covers every capability in need.
func (c ProviderCapabilities) Satisfies(need ProviderCapabilities) bool {
	return len(c.Missing(need)) == 0
}

// CapabilityReporter is implemented by providers that declare their capabilities.
type CapabilityReporter interface{ Capabilities() ProviderCapabilities }

// allCapabilities is assumed for providers that do not declare any (Replay and test fakes).
var allCapabilities = ProviderCapabilities{Text: true, Vision: true, Attachments: true, StructuredOutput: true, ToolCalls: true}

// CapabilitiesOf returns what p declares. Providers that only implement the older
// ImageCapable interface report vision through it; providers with no opinion accept everything.
func CapabilitiesOf(p Provider) ProviderCapabilities {
	if r, ok := p.(CapabilityReporter); ok {
		return r.Capabilities()
	}
	c := allCapabilities
	if ic, ok := p.(ImageCapable); ok {
		c.Vision = ic.SupportsImages()
	}
	return c
}

// ollamaVisionFamilies are local model families known to accept images. Matching is on
// the model name without its tag (llava:13b → llava).
var ollamaVisionFamilies = []string{
	"llava", "bakllava", "llama3.2-vision", "llama4", "minicpm-v", "moondream", "qwen2.5vl", "qwen2-vl",
	"qwen3-vl", "gemma3", "granite3.2-vision", "mistral-small3.1", "mistral-small3.2", "llava-llama3", "llava-phi3",
}

// ModelCapabilities is the provider capability registry: what one flavor/model can do.
//
//	claude, openai: everything (vision, PDF documents, tools)
//	ollama:         text + tools; vision for known multimodal families or OLLAMA_VISION=true
//	                (OLLAMA_VISION=false turns it off); no document blocks
func ModelCapabilities(flavor, model string) ProviderCapabilities {
	switch flavor {
	case ProviderClaude, ProviderOpenAI:
		return allCapabilities
	case ProviderOllama:
		c := ProviderCapabilities{Text: true, StructuredOutput: true, ToolCalls: true}
		switch strings.ToLower(strings.TrimSpace(config.Get("OLLAMA_VISION"))) {
		case "1", "true", "yes", "on":
			c.Vision = true
		case "0", "false", "no", "off":
		default:
			c.Vision = ollamaVisionModel(model)
		}
		return c
	}
	return ProviderCapabilities{Text: true}
}

func ollamaVisionModel(model string) bool {
	name := strings.ToLower(model)
	if i := strings.LastIndex(name, "/"); i >= 0 { // registry/namespace prefix
		name = name[i+1:]
	}
	if i := strings.Index(name, ":"); i >= 0 {
		name = name[:i]
	}
	for _, f := range ollamaVisionFamilies {
		if name == f || strings.HasPrefix(name, f+"-") {
			return true
		}
	}
	return strings.Contains(name, "vision") || strings.HasSuffix(name, "vl")
}
