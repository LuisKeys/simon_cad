// Package attachments is the attachment processor: it classifies what the user attaches,
// derives the provider capabilities each attachment needs, and — when the selected provider
// lacks them — routes the attachment work to a capable model through the model router.
//
// Attachment processing and the CAD agent need not share a model. With Ollama selected, an
// image goes to a cloud vision model that turns it into a structured interpretation (a
// sketch proposal plus a description); the local agent then works from that text:
//
//	vision model → analyze_image proposal + description → Ollama CAD agent → CAD tools
package attachments

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	vis "github.com/accelone/simoncad/internal/agent/capabilities/vision"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/tools"
)

// Kind classifies an attachment.
type Kind string

const (
	KindImage    Kind = "IMAGE"
	KindText     Kind = "TEXT"
	KindDocument Kind = "DOCUMENT"
	KindUnknown  Kind = "UNKNOWN"
)

var textExts = map[string]bool{".txt": true, ".md": true, ".markdown": true, ".json": true, ".csv": true,
	".yaml": true, ".yml": true, ".xml": true, ".tsv": true, ".ini": true, ".toml": true}

// Classify looks at the content first (magic bytes) and the file name second.
func Classify(name string, data []byte) Kind {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	ct := http.DetectContentType(head)
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case ext == ".stl":
		// A mesh is not reference text, even when it is ASCII: it goes in through Import, which
		// validates it (see internal/meshimport).
		return KindUnknown
	case ct == "image/png" || ct == "image/jpeg" || ct == "image/webp":
		return KindImage
	case ct == "application/pdf" || bytes.HasPrefix(data, []byte("%PDF-")):
		return KindDocument
	case len(data) > 0 && utf8.Valid(data) && bytes.IndexByte(data, 0) < 0 && (textExts[ext] || strings.HasPrefix(ct, "text/") || ext == ""):
		return KindText
	}
	return KindUnknown
}

// Requirements returns the provider capabilities needed to process an attachment kind.
//
//	IMAGE    → vision
//	TEXT     → text (any provider; the text is given to the agent as reference)
//	DOCUMENT → attachments (PDF document input: text extraction and page vision)
func Requirements(k Kind) (runtime.ProviderCapabilities, error) {
	switch k {
	case KindImage:
		return runtime.ProviderCapabilities{Vision: true}, nil
	case KindText:
		return runtime.ProviderCapabilities{Text: true}, nil
	case KindDocument:
		return runtime.ProviderCapabilities{Attachments: true}, nil
	}
	return runtime.ProviderCapabilities{}, fmt.Errorf("unsupported attachment type (use PNG/JPEG/WebP images, text documents or PDF; an STL model is added with Import)")
}

// Processor routes attachment work to a provider that can do it.
type Processor struct {
	Router *runtime.Router
	// Wrap, if set, wraps the routed provider (the UI releases its lock while a model runs).
	Wrap       func(runtime.Provider) runtime.Provider
	NoThinking bool
	// OnRoute observes routing decisions that fall back from the selected provider.
	OnRoute func(kind Kind, used string)
}

// Available reports whether some provider can process kind (nil) or why not.
func (p *Processor) Available(kind Kind) error {
	need, err := Requirements(kind)
	if err != nil {
		return err
	}
	_, _, err = p.Router.Route(need)
	return err
}

func (p *Processor) route(kind Kind) (runtime.Provider, error) {
	need, err := Requirements(kind)
	if err != nil {
		return nil, err
	}
	prov, fallback, err := p.Router.Route(need)
	if err != nil {
		return nil, err
	}
	reason := ""
	if fallback {
		sel := "the primary model"
		if p.Router.Selected != nil {
			sel = p.Router.Selected.Name()
		}
		reason = fmt.Sprintf("%s cannot read %s attachments", sel, strings.ToLower(string(kind)))
		if p.OnRoute != nil {
			p.OnRoute(kind, prov.Name())
		}
	}
	prov = p.Router.Observe(prov, fallback, reason)
	if p.Wrap != nil {
		prov = p.Wrap(prov)
	}
	return prov, nil
}

// Images adapts the processor to runtime.ImageInterpreter.
func (p *Processor) Images() runtime.ImageInterpreter { return imageInterpreter{p} }

type imageInterpreter struct{ p *Processor }

func (ii imageInterpreter) Available() error { return ii.p.Available(KindImage) }

// Interpret runs the vision capability (analyze_image only, which never edits the model) on
// a vision-capable provider and returns its structured description.
func (ii imageInterpreter) Interpret(ctx context.Context, a *app.App, intent string, ids []string, policy app.Policy) (*runtime.Interpretation, error) {
	prov, err := ii.p.route(KindImage)
	if err != nil {
		return nil, err
	}
	rt := &runtime.Runtime{Provider: prov, Caps: []runtime.Capability{interpreter{}}, ImageIDs: ids, NoThinking: ii.p.NoThinking, MaxSteps: 6, Subtask: true}
	c, err := rt.Begin(ctx, a, intent, policy)
	if err != nil {
		return nil, err
	}
	turn, err := c.Send(ctx, "")
	if err != nil {
		return nil, err
	}
	proposed := false
	for _, call := range turn.Calls {
		proposed = proposed || (call.Name == "analyze_image" && call.Outcome.OK && !call.Refused)
	}
	return &runtime.Interpretation{Text: strings.TrimSpace(turn.Final), Provider: prov.Name(), Usage: c.Res.Usage, Proposed: proposed}, nil
}

// interpreter is the vision capability narrowed to reading images for another agent.
type interpreter struct{}

func (interpreter) Name() string { return "attachments" }

func (interpreter) SystemPrompt() string {
	return vis.New().SystemPrompt() + "\n\n" + strings.TrimSpace(`
You are acting as the attachment processor for another CAD agent that cannot see images. Do not talk to the user and do not ask questions: the other agent continues the conversation.
- If the image is a flat 2D drawing, call analyze_image once (steps 1-4 above).
- Then answer with a compact structured description the other agent can work from, in this form:
  IMAGE: <what it shows: hand sketch / technical drawing / photo of a 3D object / other>
  PROPOSAL: <interpretation id from analyze_image, or none>
  GEOMETRY: <profiles and entities with ids, relative sizes and positions>
  DIMENSIONS: <every dimension read: value mm, what it binds to, confidence>
  MISSING: <what still has to be asked from the user>
  NOTES: <the user's request as it relates to the image, ambiguities>`)
}

func (interpreter) ToolSubset(reg *tools.Registry) []*tools.Tool {
	var out []*tools.Tool
	for _, t := range reg.Subset(tools.CapVision) {
		if t.Name == "analyze_image" {
			out = append(out, t)
		}
	}
	return out
}

func (interpreter) BuildContext(a *app.App) (string, error) { return vis.New().BuildContext(a) }

// maxDocumentTokens bounds the extracted text of one document.
const maxDocumentTokens = 8192

// ExtractDocument turns a PDF into plain text (with drawings described) on a provider that
// accepts documents. It returns the text, the model that produced it and what it consumed.
func (p *Processor) ExtractDocument(ctx context.Context, name string, data []byte) (string, string, runtime.Usage, error) {
	if k := Classify(name, data); k != KindDocument {
		return "", "", runtime.Usage{}, fmt.Errorf("%s is not a PDF document", name)
	}
	prov, err := p.route(KindDocument)
	if err != nil {
		return "", "", runtime.Usage{}, err
	}
	req := runtime.Request{NoThinking: true, MaxTokens: maxDocumentTokens, Messages: []runtime.Message{{Role: "user", Content: []runtime.Block{
		{Type: "document", Title: name, Source: &runtime.ImageSource{Type: "base64", MediaType: "application/pdf", Data: base64.StdEncoding.EncodeToString(data)}},
		{Type: "text", Text: "Extract the content of this document for a CAD engineer as plain Markdown: all text, every dimension and tolerance with units, materials, tables, notes. Describe each drawing or figure: the part's shape, its features (holes, slots, fillets...) and their dimensions and positions. Do not add commentary."},
	}}}}
	resp, err := prov.Complete(ctx, req)
	if err != nil {
		return "", "", runtime.Usage{}, err
	}
	text := strings.TrimSpace(resp.Text())
	if text == "" {
		return "", "", resp.Usage, fmt.Errorf("%s: no content could be extracted", name)
	}
	return text, prov.Name(), resp.Usage, nil
}
