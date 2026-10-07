package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"

	"github.com/accelone/simoncad/internal/agent/runtime"
)

// boxCarPNG is a 400 x 100 px dark block on white: a side silhouette of aspect 0.25.
func boxCarPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 600, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 600; x++ {
			c := color.RGBA{250, 250, 250, 255}
			if x >= 100 && x < 500 && y >= 120 && y < 220 {
				c = color.RGBA{30, 30, 40, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// smart answers structured requests by tool name and builder directives by their text.
type smart struct {
	mu sync.Mutex
	n  int
}

func (*smart) Name() string { return "claude" }
func (*smart) Capabilities() runtime.ProviderCapabilities {
	return runtime.ProviderCapabilities{Text: true, Vision: true, Attachments: true, ToolCalls: true, StructuredOutput: true}
}
func (p *smart) Complete(_ context.Context, r runtime.Request) (*runtime.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	use := func(name string, v any) *runtime.Response {
		b, _ := json.Marshal(v)
		return &runtime.Response{Content: []runtime.Block{{Type: "tool_use", ID: "u", Name: name, Input: b}}}
	}
	calls := func(cs ...string) *runtime.Response {
		var out []runtime.Block
		for i, c := range cs {
			sp := strings.Index(c, " ")
			out = append(out, runtime.Block{Type: "tool_use", ID: string(rune('a' + i)), Name: c[:sp], Input: json.RawMessage(c[sp+1:])})
		}
		return &runtime.Response{Content: out}
	}
	last := r.Messages[len(r.Messages)-1]
	if last.Content[0].Type == "tool_result" {
		return &runtime.Response{Content: []runtime.Block{{Type: "text", Text: "Done."}}}, nil
	}
	text := ""
	for _, m := range r.Messages {
		for _, b := range m.Content {
			text += b.Text
		}
	}
	has := func(name string) bool {
		for _, t := range r.Tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	switch {
	case has("submit_reference_analysis"):
		return use("submit_reference_analysis", map[string]any{"view": "left", "object_class": "car",
			"calibration": map[string]any{"azimuth": 90, "elevation": 0, "projection": "orthographic"}, "landmarks": map[string]any{}}), nil
	case has("submit_strategy"):
		return use("submit_strategy", map[string]any{"object_class": "car", "parameter_set": "vehicle", "parameters": map[string]any{"length": 4000},
			"regions": []any{}, "stages": map[string]any{}}), nil
	case has("submit_critique"):
		if strings.Contains(text, `"vehicle.height":1000`) {
			return use("submit_critique", map[string]any{"overall_score": 0.96, "largest_errors": []any{}, "good_regions": []string{"body"}, "recommended_next_actions": []string{}}), nil
		}
		return use("submit_critique", map[string]any{"overall_score": 0.5, "good_regions": []string{}, "recommended_next_actions": []string{"lower the body"},
			"largest_errors": []any{map[string]any{"region": "body", "problem": "too tall", "severity": 0.9}}}), nil
	case strings.Contains(text, "build stage BLOCKOUT"):
		return calls(`define_parameters {"set":"vehicle","parameters":{"length":4000,"width":1800,"height":1600}}`,
			`create_box {"id":"body","dx":10,"dy":10,"dz":10}`,
			`bind_parameters {"feature":"body","bindings":{"dx":"vehicle.length","dy":"vehicle.width","dz":"vehicle.height"}}`,
			`define_region {"name":"body","objects":["body"],"parameters":["vehicle.height"]}`), nil
	case strings.Contains(text, "Apply EXACTLY"):
		return calls(`set_parameter {"name":"height","value":1000}`), nil
	}
	return &runtime.Response{Content: []runtime.Block{{Type: "text", Text: "Nothing to add."}}}, nil
}

func TestVisualRefinementThroughTheService(t *testing.T) {
	s := newSvc(t)
	p := &smart{}
	s.NewProvider = func() (runtime.Provider, error) { return p, nil }
	s.NewRouter = func() *runtime.Router { return &runtime.Router{} }
	if err := s.RunRefinement(RefineRequest{Goal: "box car"}); err == nil {
		t.Fatal("a refinement without references must be refused")
	}
	info, err := s.UploadImage(boxCarPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.Hub().Subscribe()
	defer cancel()
	if err := s.RunRefinement(RefineRequest{Goal: "box car", Images: []string{info.ID}, MaxIterations: 8}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	var refineEvents int
	for len(ch) > 0 {
		if n := <-ch; n.Kind == "refine" {
			refineEvents++
		}
	}
	v := s.Visual()
	if refineEvents == 0 || v.Running || len(v.History) < 5 || v.Stage != "MATERIALS" || v.State.State != "COMPLETED" {
		t.Fatalf("events=%d running=%v iterations=%d stage=%s state=%+v", refineEvents, v.Running, len(v.History), v.Stage, v.State)
	}
	if v.Parameters["vehicle.height"] != 1000 || !v.HasMask[info.ID] || len(v.Regions) != 1 {
		t.Fatalf("view: %+v", v)
	}
	first, fix := v.History[0], v.History[1]
	if _, ok := s.RenderPNG(fix.Renders["left"]); !ok {
		t.Fatal("renders are served")
	}
	for _, prev := range []bool{false, true} {
		if b, err := s.VisualDiff(2, info.ID, prev); err != nil || len(b) == 0 {
			t.Fatalf("diff (previous=%v): %v", prev, err)
		}
	}
	if first.Scores["side_silhouette"] >= fix.Scores["side_silhouette"] {
		t.Fatalf("the correction must improve the side silhouette: %v → %v", first.Scores, fix.Scores)
	}
	st, _ := s.State()
	var summary bool
	for _, m := range st.Chat {
		summary = summary || m.Role == "agent" && strings.Contains(m.Text, "Final quality reached")
	}
	if !summary {
		t.Fatalf("chat: %+v", st.Chat)
	}

	// revert the correction: the model returns to the blockout
	if err := s.RevertIteration(2); err != nil {
		t.Fatal(err)
	}
	if v = s.Visual(); v.Parameters["vehicle.height"] != 1600 || !v.History[1].Reverted || !v.History[len(v.History)-1].Reverted || v.Stage != "BLOCKOUT" {
		t.Fatalf("after revert: %v", v.Parameters)
	}
	if err := s.AcceptIteration(2); err == nil {
		t.Fatal("a reverted iteration cannot be accepted")
	}
	if err := s.AcceptIteration(1); err != nil || !s.Visual().History[0].Accepted {
		t.Fatalf("accept: %v", err)
	}
	// region locks go through the tool path
	if err := s.SetRegionLock("body", true); err != nil || !s.Visual().Regions[0].Locked {
		t.Fatalf("lock: %v", err)
	}
	if o, _ := s.CallTool("set_parameter", json.RawMessage(`{"name":"height","value":900}`)); o.OK {
		t.Fatal("a locked region refuses edits")
	}
	if err := s.SetRegionLock("nope", true); err == nil {
		t.Fatal("unknown regions are refused")
	}
	if err := s.StopRefinement(); err == nil {
		t.Fatal("nothing is running")
	}
}
