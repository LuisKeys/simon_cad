package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/vision"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 400, 300))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func post(t *testing.T, ts string, tok, path string, body []byte) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts+path, bytes.NewReader(body))
	req.Header.Set("X-Simon-Token", tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out
}

func TestImageUploadEndpoint(t *testing.T) {
	srv, ts := testServer(t)
	data := testPNG(t)

	if code, _ := post(t, ts.URL, "", "/api/image", data); code != http.StatusForbidden {
		t.Fatalf("an upload needs the token: %d", code)
	}
	code, out := post(t, ts.URL, srv.Token, "/api/image", data)
	if code != 200 || out["width"] != 400.0 || out["height"] != 300.0 || out["media_type"] != "image/png" {
		t.Fatalf("upload: %d %v", code, out)
	}
	id := out["id"].(string)

	// round trip, as an <img src> would fetch it (token in the query)
	res, err := http.Get(ts.URL + "/api/image/" + id + "?token=" + srv.Token)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Equal(got, data) || res.Header.Get("Content-Type") != "image/png" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %q", res.StatusCode, res.Header)
	}
	if res, _ = http.Get(ts.URL + "/api/image/" + id); res.StatusCode != http.StatusForbidden {
		t.Fatalf("download without token: %d", res.StatusCode)
	}
	if res, _ = http.Get(ts.URL + "/api/image/img_nope?token=" + srv.Token); res.StatusCode != 404 {
		t.Fatalf("unknown image: %d", res.StatusCode)
	}

	// content decides the type, not the header or the name
	if code, out := post(t, ts.URL, srv.Token, "/api/image", []byte("GIF89a....")); code != 400 || !strings.Contains(out["error"].(string), "PNG, JPEG or WebP") {
		t.Fatalf("wrong type: %d %v", code, out)
	}
	if code, _ := post(t, ts.URL, srv.Token, "/api/image", nil); code != 400 {
		t.Fatalf("empty body: %d", code)
	}
	if code, _ := post(t, ts.URL, srv.Token, "/api/image", append(data[:8:8], make([]byte, vision.MaxImageBytes+1)...)); code != 400 {
		t.Fatalf("oversized: %d", code)
	}

	// retaining is a project-level choice
	if code, _ := post(t, ts.URL, srv.Token, "/api/image/"+id+"/retain", []byte(`{"keep":true}`)); code != 200 {
		t.Fatalf("retain: %d", code)
	}
	st, _ := srv.Svc.State()
	if len(st.Images) != 1 || !st.Images[0].Retained || !st.Dirty {
		t.Fatalf("state images %+v dirty=%v", st.Images, st.Dirty)
	}
	if code, _ := post(t, ts.URL, srv.Token, "/api/image/img_nope/retain", []byte(`{"keep":true}`)); code != 400 {
		t.Fatalf("retain unknown: %d", code)
	}
}

// capturing provider: records what the model is sent and answers with scripted tool calls.
type spyProvider struct {
	Responses []runtime.Response
	Reqs      []runtime.Request
}

func (p *spyProvider) Name() string { return "spy" }
func (p *spyProvider) Complete(_ context.Context, r runtime.Request) (*runtime.Response, error) {
	p.Reqs = append(p.Reqs, r)
	if len(p.Reqs) > len(p.Responses) {
		return &runtime.Response{Content: []runtime.Block{{Type: "text", Text: "Done."}}}, nil
	}
	resp := p.Responses[len(p.Reqs)-1]
	return &resp, nil
}

func toolUse(id, name, input string) runtime.Block {
	return runtime.Block{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(input)}
}

func plateProposal(img string) string {
	return `{"image_id":"` + img + `","image_size":[400,300],"confidence":0.86,
 "entities":[
  {"id":"e1","kind":"rectangle","x":50,"y":50,"w":320,"h":200,"confidence":0.95},
  {"id":"e2","kind":"circle","cx":130,"cy":150,"r":12,"confidence":0.9},
  {"id":"e3","kind":"circle","cx":290,"cy":150,"r":12,"confidence":0.9}],
 "profiles":[{"id":"p1","outer":["e1"],"holes":[["e2"],["e3"]]}],
 "dimensions":[
  {"id":"overall_width","kind":"length","binds":"e1.w","source":"missing","confidence":0},
  {"id":"thickness","kind":"depth","source":"missing","confidence":0}]}`
}

func TestImageSketchToPreviewToCommitThroughTheUI(t *testing.T) {
	s := newSvc(t)
	info, err := s.UploadImage(testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	spy := &spyProvider{Responses: []runtime.Response{
		{Content: []runtime.Block{toolUse("t1", "analyze_image", plateProposal(info.ID))}, StopReason: "tool_use"},
		{Content: []runtime.Block{{Type: "text", Text: "What are the overall width and the thickness?"}}},
	}}
	s.NewProvider = func() (runtime.Provider, error) { return spy, nil }
	if err := s.PromptWith("Make this plate", true, []string{info.ID}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	// the agent stops once the interpretation exists: the user reviews and approves it
	if len(spy.Reqs) != 1 {
		t.Fatalf("the agent must pause for the review, it made %d model calls", len(spy.Reqs))
	}
	if st, _ := s.State(); st.Busy {
		t.Fatal("the agent is idle while the interpretation waits for the user")
	}

	// the model got the image first, then the text naming it
	msg := spy.Reqs[0].Messages[len(spy.Reqs[0].Messages)-1]
	if msg.Content[0].Type != "image" || !strings.Contains(msg.Content[len(msg.Content)-1].Text, "Attached image id="+info.ID) {
		t.Fatalf("request: %+v", msg.Content)
	}
	st, _ := s.State()
	if st.Chat[0].Role != "user" || len(st.Chat[0].Images) != 1 || st.Chat[0].Images[0] != info.ID {
		t.Fatalf("the user's chat message must show its image: %+v", st.Chat[0])
	}
	if len(st.Tree) != 0 || len(st.Proposals) != 1 {
		t.Fatalf("analysis alone must not touch the model: tree=%d proposals=%d", len(st.Tree), len(st.Proposals))
	}
	pv := st.Proposals[0]
	if pv.Ready || pv.ImageW != 400 || pv.ImageH != 300 || len(pv.Report.Missing) != 2 || len(pv.Proposal.Entities) != 3 {
		t.Fatalf("proposal view: %+v", pv)
	}

	// The panel answers the questions through the tool registry, then asks for the geometry.
	for _, a := range []string{
		`{"dimension":"overall_width","value":80}`,
		`{"dimension":"thickness","value":5}`,
	} {
		o, err := s.CallTool("confirm_detected_dimension", json.RawMessage(a))
		if err != nil || !o.OK {
			t.Fatalf("confirm: %v %+v", err, o)
		}
	}
	o, err := s.CallTool("create_from_image", json.RawMessage(`{"id":"plate"}`))
	if err != nil || !o.OK || o.ChangeSet == nil || o.Committed {
		t.Fatalf("create: %v %+v", err, o)
	}
	gh, err := s.Preview(o.ChangeSet.ID)
	if err != nil || len(gh.Added) != 1 {
		t.Fatalf("preview: %v %+v", err, gh)
	}
	if st, _ = s.State(); len(st.Tree) != 0 || len(st.Pending) != 1 {
		t.Fatalf("a preview must not change the model: tree=%d pending=%d", len(st.Tree), len(st.Pending))
	}
	if _, err := s.Commit(o.ChangeSet.ID); err != nil {
		t.Fatal(err)
	}
	st, _ = s.State()
	if len(st.Tree) != 2 || st.Tree[0].Capability != "vision" || !st.Proposals[0].Ready || len(st.Proposals[0].Used) != 2 {
		t.Fatalf("after commit: tree=%+v used=%v", st.Tree, st.Proposals[0].Used)
	}
	if want := (80*50 - 2*3.141592653589793*9) * 5; st.Volume < want-1e-3 || st.Volume > want+1e-3 {
		t.Fatalf("volume %v want %v", st.Volume, want)
	}
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.State(); len(st.Tree) != 0 {
		t.Fatal("undo must remove the image-derived features")
	}
}

func TestSketchOutlinesInSceneAndGhost(t *testing.T) {
	s := newSvc(t)
	o, err := s.CallTool("create_sketch", json.RawMessage(`{"id":"sk","entities":[{"id":"c","kind":"circle","cx":0,"cy":0,"r":5}]}`))
	if err != nil || !o.OK {
		t.Fatalf("%v %+v", err, o)
	}
	gh, err := s.Preview(o.ChangeSet.ID)
	if err != nil || len(gh.Sketches) != 1 || len(gh.Sketches[0].Lines) < 12 || len(gh.Added) != 0 {
		t.Fatalf("a sketch previews as an outline, not as a body: %v %+v", err, gh)
	}
	if _, err := s.Commit(o.ChangeSet.ID); err != nil {
		t.Fatal(err)
	}
	sc, err := s.Scene(nil)
	if err != nil || len(sc.Sketches) != 1 || sc.Sketches[0].ID != "sk" || len(sc.Changed) != 0 {
		t.Fatalf("scene: %v %+v", err, sc)
	}
	// playback of a sketch-only history still draws it
	f, err := s.PlaybackFrame(1, nil)
	if err != nil || len(f.Scene.Sketches) != 1 {
		t.Fatalf("playback: %v %+v", err, f)
	}
}

func TestPromptWithImageRejectedForTextOnlyProvider(t *testing.T) {
	s := newSvc(t)
	info, _ := s.UploadImage(testPNG(t))
	s.NewProvider = func() (runtime.Provider, error) {
		return &runtime.OpenAI{Flavor: "ollama", BaseURL: "http://127.0.0.1:1", Model: "m"}, nil
	}
	s.NewRouter = func() *runtime.Router { return &runtime.Router{} } // no fallback configured
	err := s.PromptWith("x", true, []string{info.ID})
	if err == nil || !strings.Contains(err.Error(), "no fallback provider is configured") {
		t.Fatalf("got %v", err)
	}
	if err := s.PromptWith("x", true, []string{"img_nope"}); err == nil {
		t.Fatal("an image that was never uploaded must be refused")
	}
	if st, _ := s.State(); len(st.Chat) != 0 || st.Busy {
		t.Fatalf("a refused request must leave no trace: %+v", st.Chat)
	}
}
