package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/accelone/simoncad/internal/agent/runtime"
)

// capProvider is a scripted provider with declared capabilities that records its requests.
type capProvider struct {
	name  string
	caps  runtime.ProviderCapabilities
	mu    sync.Mutex
	resps []runtime.Response
	reqs  []runtime.Request
}

func (p *capProvider) Name() string                               { return p.name }
func (p *capProvider) Capabilities() runtime.ProviderCapabilities { return p.caps }
func (p *capProvider) Complete(_ context.Context, r runtime.Request) (*runtime.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, r)
	if len(p.resps) == 0 {
		return &runtime.Response{Content: []runtime.Block{{Type: "text", Text: "ok"}}}, nil
	}
	resp := p.resps[0]
	p.resps = p.resps[1:]
	return &resp, nil
}

var (
	textCaps = runtime.ProviderCapabilities{Text: true, ToolCalls: true, StructuredOutput: true}
	allCaps  = runtime.ProviderCapabilities{Text: true, Vision: true, Attachments: true, ToolCalls: true, StructuredOutput: true}
)

func TestOllamaImageIsRoutedToTheVisionFallback(t *testing.T) {
	s := newSvc(t)
	info, _ := s.UploadImage(testPNG(t))
	ollama := &capProvider{name: "ollama:llama3.1", caps: textCaps, resps: []runtime.Response{
		{Content: []runtime.Block{{Type: "text", Text: "I read the sketch; what thickness should the plate be?"}}}}}
	vision := &capProvider{name: "openai:gpt", caps: allCaps, resps: []runtime.Response{
		{Content: []runtime.Block{{Type: "text", Text: "IMAGE: sketch of a plate\nMISSING: thickness"}}}}}
	s.NewProvider = func() (runtime.Provider, error) { return ollama, nil }
	s.NewRouter = func() *runtime.Router { return &runtime.Router{Fallbacks: []runtime.Provider{vision}} }
	if err := s.PromptWith("Create this part from the attached image.", true, []string{info.ID}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if len(vision.reqs) != 1 || !runtime.HasImages(vision.reqs[0].Messages) {
		t.Fatal("the vision fallback must receive the image")
	}
	if len(ollama.reqs) != 1 || runtime.HasImages(ollama.reqs[0].Messages) {
		t.Fatal("Ollama stays the CAD agent and never receives image blocks")
	}
	if !strings.Contains(ollama.reqs[0].Messages[0].Content[0].Text, "MISSING: thickness") {
		t.Fatal("Ollama must work from the interpretation")
	}
	st, _ := s.State()
	var routed, answered bool
	for _, m := range st.Chat {
		routed = routed || m.Role == "system" && strings.Contains(m.Text, "interpreted by openai:gpt")
		answered = answered || m.Role == "agent" && strings.Contains(m.Text, "what thickness")
	}
	if !routed || !answered {
		t.Fatalf("chat: %+v", st.Chat)
	}
}

func TestImageFeaturesFollowConfiguredProviders(t *testing.T) {
	s := newSvc(t)
	s.NewProvider = func() (runtime.Provider, error) { return &capProvider{name: "ollama:llama3.1", caps: textCaps}, nil }
	s.NewRouter = func() *runtime.Router { return &runtime.Router{} }
	st, _ := s.State()
	if st.Features.Images || st.Features.Documents || st.Features.ImageReason == "" {
		t.Fatalf("no vision provider configured: %+v", st.Features)
	}
	// the selected provider stays Ollama, but a configured cloud provider enables images
	s.NewRouter = func() *runtime.Router {
		return &runtime.Router{Fallbacks: []runtime.Provider{&capProvider{name: "claude", caps: allCaps}}}
	}
	if st, _ = s.State(); !st.Features.Images || !st.Features.Documents {
		t.Fatalf("fallback configured: %+v", st.Features)
	}
	// a multimodal local model needs no fallback
	s.NewProvider = func() (runtime.Provider, error) {
		return &capProvider{name: "ollama:llava", caps: runtime.ProviderCapabilities{Text: true, Vision: true, ToolCalls: true}}, nil
	}
	s.NewRouter = func() *runtime.Router { return &runtime.Router{} }
	if st, _ = s.State(); !st.Features.Images || st.Features.Documents {
		t.Fatalf("multimodal local model: %+v", st.Features)
	}
}

func TestPDFAttachmentIsExtractedIntoASpec(t *testing.T) {
	s := newSvc(t)
	claude := &capProvider{name: "claude", caps: allCaps, resps: []runtime.Response{
		{Content: []runtime.Block{{Type: "text", Text: "Plate 80 x 60 x 4 mm, four M3 holes"}}}}}
	s.NewProvider = func() (runtime.Provider, error) { return &capProvider{name: "ollama:llama3.1", caps: textCaps}, nil }
	s.NewRouter = func() *runtime.Router { return &runtime.Router{Fallbacks: []runtime.Provider{claude}} }
	sp, err := s.AddAttachment("plate.pdf", []byte("%PDF-1.4 fake"))
	if err != nil || sp.Name != "plate.pdf" {
		t.Fatalf("pdf: %+v %v", sp, err)
	}
	if ctx := s.specContext(); !strings.Contains(ctx, "four M3 holes") || !strings.Contains(ctx, "extracted from plate.pdf by claude") {
		t.Fatalf("spec context: %s", ctx)
	}
	if _, err := s.AddAttachment("notes.md", []byte("thickness 4 mm")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAttachment("x.png", testPNG(t)); err == nil {
		t.Fatal("images are not documents")
	}
	s.NewRouter = func() *runtime.Router { return &runtime.Router{} }
	if _, err := s.AddAttachment("b.pdf", []byte("%PDF-1.4 fake")); err == nil {
		t.Fatal("a PDF without a document-capable provider must be refused")
	}
}

func TestComplexPromptRunsTheWorkflowAndReportsState(t *testing.T) {
	s := newSvc(t)
	plan := `{"goal":"Plate with boss","steps":[{"step":1,"operation":"primitive","description":"Create base plate","ref":"plate"},{"step":2,"operation":"primitive","description":"Add cylindrical boss","ref":"boss","depends_on":[1]}],"expectations":{"objects":[{"ref":"plate","size":{"x":100,"y":60,"z":4}}]}}`
	use := func(id, name, args string) runtime.Response {
		return runtime.Response{Content: []runtime.Block{{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(args)}}}
	}
	text := func(t string) runtime.Response {
		return runtime.Response{Content: []runtime.Block{{Type: "text", Text: t}}}
	}
	prov := &capProvider{name: "claude", caps: allCaps, resps: []runtime.Response{
		use("p", "submit_plan", plan),
		use("1", "create_box", `{"id":"plate","dx":100,"dy":60,"dz":4}`), text("Created plate."),
		use("2", "create_cylinder", `{"id":"boss","radius":5,"height":10,"x":50,"y":30,"z":4}`), text("Added boss."),
	}}
	s.NewProvider = func() (runtime.Provider, error) { return prov, nil }
	ch, cancel := s.Hub().Subscribe()
	defer cancel()
	if err := s.Prompt("Create a 100 x 60 x 4 mm base plate, add a cylindrical boss on top and fillet the plate edges", false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	var seen []string
	for len(ch) > 0 {
		if n := <-ch; n.Kind == "agent_state" {
			seen = append(seen, string(n.State.State))
		}
	}
	got := strings.Join(seen, ">")
	if !strings.HasPrefix(got, "PLANNING>EXECUTING>EXECUTING>VALIDATING") || !strings.HasSuffix(got, "COMPLETED") {
		t.Fatalf("agent states: %s", got)
	}
	st, _ := s.State()
	var planned bool
	for _, m := range st.Chat {
		planned = planned || m.Role == "plan" && strings.Contains(m.Text, "1. Create base plate")
	}
	if !planned || len(st.Draft) != 2 || st.AgentState == nil || st.AgentState.State != "COMPLETED" {
		t.Fatalf("planned=%v draft=%v state=%+v", planned, st.Draft, st.AgentState)
	}
	// the plan message is not replayed to the model as conversation
	for _, m := range s.history() {
		if strings.Contains(m.Content[0].Text, "Plan:") {
			t.Fatal("plan messages must not enter the model history")
		}
	}
}

func TestUserMessageListsTheDocumentsAndImagesItCarries(t *testing.T) {
	s := newSvc(t)
	s.NewProvider = func() (runtime.Provider, error) { return &capProvider{name: "claude", caps: allCaps}, nil }
	for _, n := range []string{"a.md", "b.txt"} {
		if _, err := s.AddAttachment(n, []byte("spec "+n)); err != nil {
			t.Fatal(err)
		}
	}
	var imgs []string
	for i := 0; i < 6; i++ { // more than one image per message
		info, err := s.UploadImage(testPNG(t))
		if err != nil {
			t.Fatal(err)
		}
		imgs = append(imgs, info.ID)
	}
	if err := s.PromptWith("use these", false, imgs[:1]); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if _, err := s.AddAttachment("c.md", []byte("spec c")); err != nil {
		t.Fatal(err)
	}
	if err := s.PromptWith("and this", false, nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	st, _ := s.State()
	var users []ChatMessage
	for _, m := range st.Chat {
		if m.Role == "user" {
			users = append(users, m)
		}
	}
	if len(users) != 2 || strings.Join(users[0].Docs, ",") != "a.md,b.txt" || strings.Join(users[1].Docs, ",") != "c.md" {
		t.Fatalf("docs per message: %+v", users)
	}
	if err := s.PromptWith("too many", false, append(imgs, imgs...)[:maxPromptImages+1]); err == nil {
		t.Fatal("the image bound must hold")
	}
}

// identified is a provider double that names its provider and model, so the router knows
// whether it runs locally.
type identified struct {
	*capProvider
	provider, model string
}

func (p identified) Identity() (string, string) { return p.provider, p.model }

// A local primary asks before its first remote fallback; once authorized, the request runs,
// the fallback is named in the conversation and the model indicator follows the model that
// ran last.
func TestRemoteFallbackIsAuthorizedFirst(t *testing.T) {
	s := newSvc(t)
	info, _ := s.UploadImage(testPNG(t))
	ollama := identified{&capProvider{name: "ollama:qwen3", caps: textCaps, resps: []runtime.Response{
		{Content: []runtime.Block{{Type: "text", Text: "done"}}}}}, "ollama", "qwen3"}
	vision := identified{&capProvider{name: "openai:gpt-x", caps: allCaps, resps: []runtime.Response{
		{Content: []runtime.Block{{Type: "text", Text: "IMAGE: plate"}}}}}, "openai", "gpt-x"}
	s.NewProvider = func() (runtime.Provider, error) { return ollama, nil }
	s.NewRouter = func() *runtime.Router { return &runtime.Router{Fallbacks: []runtime.Provider{vision}} }
	var models []runtime.RouteEvent
	ch, cancel := s.Hub().Subscribe()
	defer cancel()

	err := s.PromptWith("Model this.", true, []string{info.ID})
	if !errors.Is(err, runtime.ErrConsentRequired) || len(vision.reqs) != 0 {
		t.Fatalf("expected a consent request, got %v (vision calls %d)", err, len(vision.reqs))
	}
	if st, _ := s.State(); !st.Features.Images || st.Primary == nil || st.Primary.Label() != "Ollama · qwen3" || !st.Primary.Local {
		t.Fatalf("state before authorizing: %+v %+v", st.Features, st.Primary)
	}
	if err := s.AuthorizeFallback(true, false); err != nil {
		t.Fatal(err)
	}
	if err := s.PromptWith("Model this.", true, []string{info.ID}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	for drained := false; !drained; {
		select {
		case n := <-ch:
			if n.Kind == "model" {
				models = append(models, *n.Model)
			}
		default:
			drained = true
		}
	}
	if len(models) != 2 || models[0].Label() != "OpenAI · gpt-x" || !models[0].Fallback || models[0].Requested != "qwen3" ||
		models[1].Label() != "Ollama · qwen3" || models[1].Fallback {
		t.Fatalf("model indicator events: %+v", models)
	}
	st, _ := s.State()
	if st.Model == nil || st.Model.Label() != "Ollama · qwen3" {
		t.Fatalf("the last response came from the local model: %+v", st.Model)
	}
	n := 0
	for _, m := range st.Chat {
		if m.Role == "system" && strings.HasPrefix(m.Text, "Fallback: OpenAI · gpt-x") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the fallback is named once: %+v", st.Chat)
	}
}

func TestConsentRequestIsAConflictWithItsDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	fail(rec, &runtime.ConsentError{Selected: "ollama:qwen3", Fallback: runtime.RouteEvent{Provider: "openai", Model: "gpt-x", Fallback: true}})
	var body struct {
		Code     string             `json:"code"`
		Fallback runtime.RouteEvent `json:"fallback"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || rec.Code != http.StatusConflict ||
		body.Code != "fallback_consent" || body.Fallback.Model != "gpt-x" {
		t.Fatalf("%d %+v %v", rec.Code, body, err)
	}
	rec = httptest.NewRecorder()
	fail(rec, &runtime.ConsentError{Declined: true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a declined fallback is a plain error: %d", rec.Code)
	}
}
