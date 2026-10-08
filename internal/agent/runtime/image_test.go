package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
)

func pngData(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 40, 30))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// capture records the requests a turn sends to the model.
type capture struct {
	reqs []runtime.Request
}

func (c *capture) Name() string { return "capture" }
func (c *capture) Complete(_ context.Context, r runtime.Request) (*runtime.Response, error) {
	c.reqs = append(c.reqs, r)
	return &runtime.Response{Content: []runtime.Block{{Type: "text", Text: "ok"}}}, nil
}

func TestImageIsAttachedToTheUserMessageAndRecorded(t *testing.T) {
	a := app.New("t", time.Now)
	defer a.Close()
	im, err := a.Images.Put(pngData(t))
	if err != nil {
		t.Fatal(err)
	}
	cp := &capture{}
	rt := &runtime.Runtime{Provider: cp, Caps: caps(), ImageIDs: []string{im.ID}}
	if _, err := rt.Run(context.Background(), a, "build this", app.PolicyAuto); err != nil {
		t.Fatal(err)
	}
	msg := cp.reqs[0].Messages[len(cp.reqs[0].Messages)-1]
	if msg.Role != "user" || len(msg.Content) != 2 || msg.Content[0].Type != "image" || msg.Content[1].Type != "text" {
		t.Fatalf("image blocks must precede the text: %+v", msg.Content)
	}
	src := msg.Content[0].Source
	if src == nil || src.Type != "base64" || src.MediaType != "image/png" || src.Data == "" {
		t.Fatalf("image source: %+v", src)
	}
	if !strings.Contains(msg.Content[1].Text, "Attached image id="+im.ID+" (40x30 px)") {
		t.Fatalf("the model must be told the image id and size:\n%s", msg.Content[1].Text)
	}
	if got := a.AH.Prompts[0].Images; len(got) != 1 || got[0] != im.ID {
		t.Fatalf("prompt must record its images: %+v", got)
	}
	// the vision capability's tools are offered
	names := map[string]bool{}
	for _, d := range cp.reqs[0].Tools {
		names[d.Name] = true
	}
	for _, want := range []string{"analyze_image", "create_from_image", "confirm_detected_dimension", "set_sketch_scale", "create_box"} {
		if !names[want] {
			t.Errorf("tool %s not offered", want)
		}
	}
}

func TestUnknownImageAndUnsupportedProviderFailBeforeTheModelRuns(t *testing.T) {
	a := app.New("t", time.Now)
	defer a.Close()
	cp := &capture{}
	if _, err := (&runtime.Runtime{Provider: cp, Caps: caps(), ImageIDs: []string{"img_nope"}}).Run(context.Background(), a, "x", app.PolicyAuto); err == nil {
		t.Fatal("unknown image must fail")
	}
	im, _ := a.Images.Put(pngData(t))
	ollama := &runtime.OpenAI{Flavor: "ollama", BaseURL: "http://127.0.0.1:1", Model: "m"}
	_, err := (&runtime.Runtime{Provider: ollama, Caps: caps(), ImageIDs: []string{im.ID}}).Run(context.Background(), a, "x", app.PolicyAuto)
	if err == nil || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("ollama must be refused with a clear error: %v", err)
	}
	if len(cp.reqs) != 0 || len(a.AH.Prompts) != 0 {
		t.Fatal("nothing must be recorded or sent when the turn cannot run")
	}
	// the wrapper defers to the provider it wraps
	if (&runtime.Recorder{Inner: ollama}).SupportsImages() {
		t.Fatal("recorder must report the inner provider's capability")
	}
	if !(&runtime.Recorder{Inner: cp}).SupportsImages() {
		t.Fatal("a provider without an opinion is assumed to accept images")
	}
}

func TestClaudeSendsImageBlocks(t *testing.T) {
	var got struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	}))
	defer srv.Close()
	c := &runtime.Claude{APIKey: "k", BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	_, err := c.Complete(context.Background(), runtime.Request{Messages: []runtime.Message{{Role: "user", Content: []runtime.Block{
		{Type: "image", Source: &runtime.ImageSource{Type: "base64", MediaType: "image/png", Data: "QUJD"}},
		{Type: "text", Text: "look"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	img := got.Messages[0].Content[0]
	src := img["source"].(map[string]any)
	if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != "QUJD" {
		t.Fatalf("wire image block: %v", img)
	}
	if _, has := img["text"]; has {
		t.Fatalf("an image block must not carry text fields: %v", img)
	}
	// oversized images are refused locally
	big := strings.Repeat("A", 7<<20)
	if _, err := c.Complete(context.Background(), runtime.Request{Messages: []runtime.Message{{Role: "user", Content: []runtime.Block{
		{Type: "image", Source: &runtime.ImageSource{Type: "base64", MediaType: "image/png", Data: big}}}}}}); err == nil {
		t.Fatal("an image over 5 MB must be refused")
	}
}

func TestOpenAIResponsesSendsInputImage(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"status":"completed","output":[],"usage":{}}`))
	}))
	defer srv.Close()
	o := &runtime.OpenAI{Flavor: "openai", APIKey: "k", BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	_, err := o.Complete(context.Background(), runtime.Request{Messages: []runtime.Message{
		{Role: "user", Content: []runtime.Block{
			{Type: "image", Source: &runtime.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "QUJD"}},
			{Type: "text", Text: "look"}}},
		{Role: "assistant", Content: []runtime.Block{{Type: "text", Text: "seen"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	in := got["input"].([]any)
	parts := in[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["type"] != "input_image" ||
		parts[0].(map[string]any)["image_url"] != "data:image/jpeg;base64,QUJD" ||
		parts[1].(map[string]any)["type"] != "input_text" {
		t.Fatalf("responses content parts: %v", parts)
	}
	if s, ok := in[1].(map[string]any)["content"].(string); !ok || s != "seen" {
		t.Fatalf("text-only messages keep the plain string form: %v", in[1])
	}
}
