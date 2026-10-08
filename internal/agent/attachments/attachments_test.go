package attachments_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/agent/attachments"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 48))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestClassifyAndRequirements(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want attachments.Kind
	}{
		{"sketch.png", pngBytes(t), attachments.KindImage},
		{"no-extension", pngBytes(t), attachments.KindImage},
		{"spec.md", []byte("# Servo bracket\nM3 holes"), attachments.KindText},
		{"dims.json", []byte(`{"dx": 80}`), attachments.KindText},
		{"drawing.pdf", []byte("%PDF-1.7\n%âãÏÓ\n1 0 obj"), attachments.KindDocument},
		{"blob.bin", []byte{0, 1, 2, 3, 0xff}, attachments.KindUnknown},
	} {
		if got := attachments.Classify(c.name, c.data); got != c.want {
			t.Errorf("Classify(%s) = %s, want %s", c.name, got, c.want)
		}
	}
	if need, _ := attachments.Requirements(attachments.KindImage); !need.Vision {
		t.Error("images need vision")
	}
	if need, _ := attachments.Requirements(attachments.KindDocument); !need.Attachments {
		t.Error("PDFs need document attachments")
	}
	if need, _ := attachments.Requirements(attachments.KindText); need.Vision || need.Attachments {
		t.Error("text works on any provider")
	}
	if _, err := attachments.Requirements(attachments.KindUnknown); err == nil {
		t.Error("unknown attachments are refused")
	}
}

type fake struct {
	name  string
	caps  runtime.ProviderCapabilities
	reply string
	reqs  []runtime.Request
}

func (f *fake) Name() string                               { return f.name }
func (f *fake) Capabilities() runtime.ProviderCapabilities { return f.caps }
func (f *fake) Complete(_ context.Context, r runtime.Request) (*runtime.Response, error) {
	f.reqs = append(f.reqs, r)
	return &runtime.Response{Content: []runtime.Block{{Type: "text", Text: f.reply}}}, nil
}

var (
	textOnly = runtime.ProviderCapabilities{Text: true, ToolCalls: true}
	all      = runtime.ProviderCapabilities{Text: true, Vision: true, Attachments: true, ToolCalls: true}
)

func TestImagesAreInterpretedByTheFallbackVisionModel(t *testing.T) {
	a := app.New("t", time.Now)
	defer a.Close()
	im, err := a.Images.Put(pngBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	ollama := &fake{name: "ollama:llama3.1", caps: textOnly}
	vision := &fake{name: "openai:gpt", caps: all, reply: "IMAGE: hand sketch\nPROPOSAL: none"}
	var routed []string
	p := &attachments.Processor{Router: &runtime.Router{Selected: ollama, Fallbacks: []runtime.Provider{vision}},
		OnRoute: func(k attachments.Kind, used string) { routed = append(routed, string(k)+"→"+used) }}
	ii := p.Images()
	if err := ii.Available(); err != nil {
		t.Fatal(err)
	}
	in, err := ii.Interpret(context.Background(), a, "make this part", []string{im.ID}, app.PolicyAuto)
	if err != nil {
		t.Fatal(err)
	}
	if in.Provider != "openai:gpt" || !strings.HasPrefix(in.Text, "IMAGE: hand sketch") || len(ollama.reqs) != 0 {
		t.Fatalf("interpretation: %+v (ollama requests %d)", in, len(ollama.reqs))
	}
	req := vision.reqs[0]
	if !runtime.HasImages(req.Messages) || len(req.Tools) != 1 || req.Tools[0].Name != "analyze_image" {
		t.Fatalf("the vision model gets the image and only analyze_image: %d tools", len(req.Tools))
	}
	if len(routed) != 1 || routed[0] != "IMAGE→openai:gpt" || len(a.AH.Prompts) != 0 {
		t.Fatalf("routed=%v prompts=%d (a subtask records no prompt)", routed, len(a.AH.Prompts))
	}
	// nothing configured that can see
	p.Router.Fallbacks = nil
	if err := p.Images().Available(); err == nil {
		t.Fatal("images must be unavailable without a vision-capable provider")
	}
}

func TestPDFIsExtractedByADocumentCapableModel(t *testing.T) {
	ollama := &fake{name: "ollama:llama3.1", caps: textOnly}
	claude := &fake{name: "claude", caps: all, reply: "# Bracket\nThickness 4 mm"}
	p := &attachments.Processor{Router: &runtime.Router{Selected: ollama, Fallbacks: []runtime.Provider{claude}}}
	text, model, _, err := p.ExtractDocument(context.Background(), "bracket.pdf", []byte("%PDF-1.4 ..."))
	if err != nil || model != "claude" || !strings.Contains(text, "Thickness 4 mm") {
		t.Fatalf("extract: %q %q %v", text, model, err)
	}
	doc := claude.reqs[0].Messages[0].Content[0]
	if doc.Type != "document" || doc.Title != "bracket.pdf" || doc.Source.MediaType != "application/pdf" {
		t.Fatalf("document block: %+v", doc)
	}
	if _, _, _, err := p.ExtractDocument(context.Background(), "notes.txt", []byte("plain")); err == nil {
		t.Fatal("only PDFs are extracted")
	}
}
