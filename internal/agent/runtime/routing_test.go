package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
)

func TestCapabilityRegistry(t *testing.T) {
	t.Setenv("OLLAMA_VISION", "")
	for model, vision := range map[string]bool{
		"llama3.1": false, "qwen2.5-coder:14b": false, "mistral": false,
		"llava:13b": true, "llama3.2-vision": true, "qwen2.5vl:7b": true, "library/minicpm-v": true, "gemma3:12b": true,
	} {
		c := runtime.ModelCapabilities(runtime.ProviderOllama, model)
		if c.Vision != vision || !c.Text || !c.ToolCalls || c.Attachments {
			t.Errorf("ollama %s: %+v (want vision=%v)", model, c, vision)
		}
	}
	t.Setenv("OLLAMA_VISION", "true")
	if !runtime.ModelCapabilities(runtime.ProviderOllama, "my-custom-model").Vision {
		t.Error("OLLAMA_VISION=true must enable vision")
	}
	t.Setenv("OLLAMA_VISION", "off")
	if runtime.ModelCapabilities(runtime.ProviderOllama, "llava").Vision {
		t.Error("OLLAMA_VISION=off must disable vision")
	}
	for _, p := range []string{runtime.ProviderClaude, runtime.ProviderOpenAI} {
		if c := runtime.ModelCapabilities(p, "x"); !c.Vision || !c.Attachments || !c.ToolCalls {
			t.Errorf("%s: %+v", p, c)
		}
	}
	// the OpenAI-compatible provider reports through the registry
	t.Setenv("OLLAMA_VISION", "")
	if (&runtime.OpenAI{Flavor: "ollama", Model: "llama3.1"}).SupportsImages() || !(&runtime.OpenAI{Flavor: "ollama", Model: "llava"}).SupportsImages() {
		t.Error("ollama image support must follow the model")
	}
}

type named struct {
	name string
	caps runtime.ProviderCapabilities
}

func (n named) Name() string                                                       { return n.name }
func (n named) Capabilities() runtime.ProviderCapabilities                         { return n.caps }
func (named) Complete(context.Context, runtime.Request) (*runtime.Response, error) { return nil, nil }

func TestRouterFallbackPriority(t *testing.T) {
	text := runtime.ProviderCapabilities{Text: true, ToolCalls: true}
	all := runtime.ProviderCapabilities{Text: true, Vision: true, Attachments: true, ToolCalls: true}
	ollama, openai, claude := named{"ollama", text}, named{"openai", all}, named{"claude", all}
	vision := runtime.ProviderCapabilities{Vision: true}

	r := &runtime.Router{Selected: ollama, Fallbacks: []runtime.Provider{openai, claude}}
	if p, fb, err := r.Route(runtime.ProviderCapabilities{Text: true}); err != nil || fb || p.Name() != "ollama" {
		t.Fatalf("text stays on the selected provider: %v %v %v", p, fb, err)
	}
	if p, fb, err := r.Route(vision); err != nil || !fb || p.Name() != "openai" {
		t.Fatalf("vision falls back to OpenAI first: %v %v %v", p, fb, err)
	}
	r.Fallbacks = []runtime.Provider{claude}
	if p, _, err := r.Route(vision); err != nil || p.Name() != "claude" {
		t.Fatalf("then Claude: %v %v", p, err)
	}
	r.Fallbacks = nil
	_, _, err := r.Route(vision)
	if !errors.Is(err, runtime.ErrCapabilityUnavailable) || !strings.Contains(err.Error(), "vision") {
		t.Fatalf("no capable provider: %v", err)
	}
	// a multimodal selected provider is used directly
	r = &runtime.Router{Selected: named{"llava", all}, Fallbacks: []runtime.Provider{openai}}
	if p, fb, _ := r.Route(vision); fb || p.Name() != "llava" {
		t.Fatal("a vision-capable selected provider must win")
	}
}

func TestRouterFromEnv(t *testing.T) {
	t.Setenv("SIMONCAD_PROVIDER", "ollama")
	t.Setenv("OLLAMA_MODEL", "llama3.1")
	t.Setenv("OLLAMA_VISION", "")
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "gpt-x")
	t.Setenv("ANTHROPIC_API_KEY", "a")
	t.Setenv("SIMONCAD_FALLBACK_ENABLED", "")
	t.Setenv("SIMONCAD_FALLBACK_PROVIDER", "")
	// A configuration that predates the fallback slot migrates to its first complete cloud
	// provider: the single fallback model.
	r := runtime.NewRouterFromEnv()
	if r.Selected == nil || r.Selected.Name() != "ollama:llama3.1" || len(r.Fallbacks) != 1 || r.Fallbacks[0].Name() != "openai:gpt-x" {
		t.Fatalf("router: %+v", r)
	}
	t.Setenv("SIMONCAD_FALLBACK_ENABLED", "true") // an explicit choice wins over the migration
	t.Setenv("SIMONCAD_FALLBACK_PROVIDER", "claude")
	if r = runtime.NewRouterFromEnv(); len(r.Fallbacks) != 1 || r.Fallbacks[0].Name() != "claude-sonnet-5-5" {
		t.Fatalf("explicit fallback: %+v", r)
	}
	t.Setenv("SIMONCAD_FALLBACK_ENABLED", "false")
	r = runtime.NewRouterFromEnv()
	if _, _, err := r.Route(runtime.ProviderCapabilities{Vision: true}); len(r.Fallbacks) != 0 || err == nil || !strings.Contains(err.Error(), "fallback model is disabled") {
		t.Fatalf("disabled fallback: %+v %v", r, err)
	}
	t.Setenv("SIMONCAD_FALLBACK_ENABLED", "")
	t.Setenv("SIMONCAD_FALLBACK_PROVIDER", "")
	t.Setenv("OPENAI_MODEL", "") // incomplete OpenAI configuration is not a fallback
	t.Setenv("ANTHROPIC_API_KEY", "")
	r = runtime.NewRouterFromEnv()
	if len(r.Fallbacks) != 0 || r.Supports(runtime.ProviderCapabilities{Vision: true}) {
		t.Fatalf("no fallback expected: %+v", r.Fallbacks)
	}
}

func TestOpenAIResponsesSendsInputFile(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"text"}]}]}`))
	}))
	defer srv.Close()
	o := &runtime.OpenAI{Flavor: "openai", BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	if _, err := o.Complete(context.Background(), runtime.Request{Messages: []runtime.Message{{Role: "user", Content: []runtime.Block{
		{Type: "document", Title: "spec.pdf", Source: &runtime.ImageSource{Type: "base64", MediaType: "application/pdf", Data: "JVBE"}},
		{Type: "text", Text: "extract"}}}}}); err != nil {
		t.Fatal(err)
	}
	part := got["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if part["type"] != "input_file" || part["filename"] != "spec.pdf" || part["file_data"] != "data:application/pdf;base64,JVBE" {
		t.Fatalf("input_file part: %v", part)
	}
	ollama := &runtime.OpenAI{Flavor: "ollama", BaseURL: srv.URL, Model: "llama3.1", HTTP: srv.Client()}
	if _, err := ollama.Complete(context.Background(), runtime.Request{Messages: []runtime.Message{{Role: "user", Content: []runtime.Block{
		{Type: "document", Source: &runtime.ImageSource{MediaType: "application/pdf", Data: "JVBE"}}}}}}); err == nil {
		t.Fatal("ollama must refuse document blocks")
	}
}

// interp is a scripted ImageInterpreter.
type interp struct {
	err   error
	calls int
}

func (i *interp) Available() error { return i.err }
func (i *interp) Interpret(_ context.Context, _ *app.App, _ string, ids []string, _ app.Policy) (*runtime.Interpretation, error) {
	i.calls++
	return &runtime.Interpretation{Text: "IMAGE: hand sketch of a 2-hole plate\nPROPOSAL: interp_1", Provider: "openai:vision"}, nil
}

func TestTextOnlyProviderWorksFromTheImageInterpretation(t *testing.T) {
	a := app.New("t", time.Now)
	defer a.Close()
	im, _ := a.Images.Put(pngData(t))
	t.Setenv("OLLAMA_VISION", "")
	local := &textOnly{}
	ip := &interp{}
	res, err := (&runtime.Runtime{Provider: local, Caps: caps(), ImageIDs: []string{im.ID}, Interpreter: ip}).Run(context.Background(), a, "make this plate", app.PolicyAuto)
	if err != nil {
		t.Fatal(err)
	}
	if ip.calls != 1 || len(local.reqs) != 1 || res.Final != "ok" {
		t.Fatalf("calls=%d reqs=%d final=%q", ip.calls, len(local.reqs), res.Final)
	}
	if runtime.HasImages(local.reqs[0].Messages) {
		t.Fatal("the text-only provider must not receive image blocks")
	}
	text := local.reqs[0].Messages[0].Content[0].Text
	if !strings.Contains(text, "PROPOSAL: interp_1") || !strings.Contains(text, "interpreted it for you") {
		t.Fatalf("interpretation missing from the context:\n%s", text)
	}
	if len(a.AH.Prompts) != 1 || len(a.AH.Prompts[0].Images) != 1 {
		t.Fatalf("one prompt with its image must be recorded: %+v", a.AH.Prompts)
	}
	// an interpreter that cannot run refuses the turn before anything is recorded
	b := app.New("t", time.Now)
	defer b.Close()
	im2, _ := b.Images.Put(pngData(t))
	_, err = (&runtime.Runtime{Provider: &textOnly{}, Caps: caps(), ImageIDs: []string{im2.ID}, Interpreter: &interp{err: errors.New("no vision model")}}).Run(context.Background(), b, "x", app.PolicyAuto)
	if err == nil || len(b.AH.Prompts) != 0 {
		t.Fatalf("unavailable interpreter: %v, prompts %d", err, len(b.AH.Prompts))
	}
}

// textOnly is a capture provider that declares no vision.
type textOnly struct{ capture }

func (*textOnly) Capabilities() runtime.ProviderCapabilities {
	return runtime.ProviderCapabilities{Text: true, ToolCalls: true}
}
