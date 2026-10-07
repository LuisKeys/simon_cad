package refine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/visual"
)

// sideRef draws a car-like side silhouette: a 400 x 100 px dark block on white (aspect 0.25).
func sideRef(t *testing.T) []byte {
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

// fake answers by looking at the request: structured tools by name, builder directives by text.
type fake struct {
	mu       sync.Mutex
	name     string
	respond  func(req runtime.Request, last string) runtime.Response
	requests []runtime.Request
}

func (f *fake) Name() string { return f.name }
func (f *fake) Complete(_ context.Context, r runtime.Request) (*runtime.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	m := r.Messages[len(r.Messages)-1]
	if len(m.Content) > 0 && m.Content[0].Type == "tool_result" {
		return &runtime.Response{Content: []runtime.Block{{Type: "text", Text: "Done."}}}, nil
	}
	last := ""
	for _, b := range m.Content {
		if b.Type == "text" {
			last = b.Text
		}
	}
	resp := f.respond(r, last)
	return &resp, nil
}

func calls(cs ...string) runtime.Response {
	var out []runtime.Block
	for i, c := range cs {
		sp := strings.Index(c, " ")
		out = append(out, runtime.Block{Type: "tool_use", ID: fmt.Sprintf("t%d", i), Name: c[:sp], Input: json.RawMessage(c[sp+1:])})
	}
	return runtime.Response{Content: out}
}

func submit(name string, v any) runtime.Response {
	b, _ := json.Marshal(v)
	return runtime.Response{Content: []runtime.Block{{Type: "tool_use", ID: "s", Name: name, Input: b}}}
}

func hasTool(r runtime.Request, name string) bool {
	for _, t := range r.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// newRun wires a controller whose vision model suggests heightFix for the body height.
func newRun(t *testing.T, heightFix float64) (*Controller, *app.App, *fake, *[]Event) {
	t.Helper()
	a := app.New("car", time.Now)
	t.Cleanup(a.Close)
	vision := &fake{name: "vision", respond: func(r runtime.Request, last string) runtime.Response {
		switch {
		case hasTool(r, "submit_reference_analysis"):
			return submit("submit_reference_analysis", map[string]any{"view": "left", "object_class": "car",
				"calibration":         map[string]any{"azimuth": 90, "elevation": 0, "projection": "orthographic"},
				"landmarks":           map[string]any{"front_tip": []float64{0.17, 0.6}, "rear_tip": []float64{0.83, 0.6}},
				"known_dimensions_mm": map[string]any{"length": 4000}})
		case hasTool(r, "submit_critique"):
			all := ""
			for _, m := range r.Messages {
				for _, b := range m.Content {
					all += b.Text
				}
			}
			if strings.Contains(all, `"vehicle.height":1000`) {
				return submit("submit_critique", map[string]any{"overall_score": 0.95, "largest_errors": []any{}, "good_regions": []string{"body"}, "recommended_next_actions": []string{}})
			}
			return submit("submit_critique", map[string]any{"overall_score": 0.5, "largest_errors": []any{
				map[string]any{"region": "body", "problem": "body is too tall", "severity": 0.9, "estimated_correction": map[string]any{"parameter": "vehicle.height", "value": heightFix}},
			}, "good_regions": []string{}, "recommended_next_actions": []string{"lower the body"}})
		}
		t.Fatalf("unexpected vision request: %s", last)
		return runtime.Response{}
	}}
	builder := &fake{name: "builder", respond: func(r runtime.Request, last string) runtime.Response {
		switch {
		case hasTool(r, "submit_strategy"):
			return submit("submit_strategy", map[string]any{"object_class": "car", "parameter_set": "vehicle",
				"parameters": map[string]any{"length": 4000, "width": 1800, "height": 1600},
				"regions":    []any{map[string]any{"name": "body", "objects": []string{"body"}, "parameters": []string{"vehicle.height"}}},
				"stages":     map[string]any{"BLOCKOUT": []string{"body block"}}})
		case strings.Contains(last, "build stage BLOCKOUT"):
			return calls(`define_parameters {"set":"vehicle","parameters":{"length":4000,"width":1800,"height":1600}}`,
				`create_box {"id":"body","dx":100,"dy":100,"dz":100}`,
				`bind_parameters {"feature":"body","bindings":{"dx":"vehicle.length","dy":"vehicle.width","dz":"vehicle.height"}}`,
				`define_region {"name":"body","objects":["body"],"parameters":["vehicle.height"]}`)
		case strings.Contains(last, "build stage MATERIALS"):
			return calls(`set_color {"features":["body"],"color":"#C00000"}`)
		case strings.Contains(last, "build stage"):
			return runtime.Response{Content: []runtime.Block{{Type: "text", Text: "Nothing to add for this simple block."}}}
		case strings.Contains(last, "Apply EXACTLY"):
			v := heightFix
			return calls(fmt.Sprintf(`set_parameter {"name":"vehicle.height","value":%g}`, v), `set_parameter {"name":"vehicle.width","value":1700}`,
				`set_parameter {"name":"vehicle.length","value":4000}`, `set_parameter {"name":"vehicle.width","value":1750}`)
		}
		return runtime.Response{Content: []runtime.Block{{Type: "text", Text: "ok"}}}
	}}
	var events []Event
	c := &Controller{App: a, Analyzer: &Analyzer{Vision: vision}, Planner: &Planner{Provider: builder}, Builder: &Builder{Provider: builder},
		Critic: &VisionCritic{Vision: vision}, MaxIterations: 8, RenderSize: 256,
		OnEvent: func(e Event) { events = append(events, e) }}
	return c, a, builder, &events
}

func TestRefinementCorrectsTheSilhouetteAndAdvancesStages(t *testing.T) {
	c, a, builder, events := newRun(t, 1000)
	im, err := a.Images.Put(sideRef(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Run(context.Background(), "a simple box car", []string{im.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != StateCompleted {
		t.Fatalf("state %s: %s", res.State, res.Summary)
	}
	vs := a.Visual
	ref := vs.Reference(im.ID)
	if ref.Calibration.View != "left" || ref.Mask() == nil || vs.ReferenceGeometry["side_aspect"] != 0.25 {
		t.Fatalf("reference analysis: %+v %v", ref.Calibration, vs.ReferenceGeometry)
	}
	h := vs.History
	if len(h) < 5 || h[0].Kind != "build" || h[0].Scores[visual.MetricSide] > 0.8 {
		t.Fatalf("history: %d iterations, first %+v", len(h), h[0])
	}
	fix := h[1]
	if fix.Kind != "refine" || fix.Reverted || fix.ParametersBefore["vehicle.height"] != 1600 || fix.ParametersAfter["vehicle.height"] != 1000 ||
		fix.Scores[visual.MetricSide] < 0.95 || len(fix.Changes) != 1 {
		t.Fatalf("correction iteration: %+v", fix)
	}
	// at most 3 structural changes were applied although the builder tried 4
	if len(fix.ChangeSets) != 3 {
		t.Fatalf("structural budget: %v", fix.ChangeSets)
	}
	stages := map[string]bool{}
	for _, it := range h {
		stages[it.Stage] = true
		if it.Checkpoint == "" || len(it.Renders) < 5 {
			t.Fatalf("iteration %d lacks checkpoint/renders: %+v", it.N, it)
		}
	}
	if !stages["MATERIALS"] || vs.Stage != 3 {
		t.Fatalf("stages reached: %v (stage %d)", stages, vs.Stage)
	}
	// the builder never saw images: the critic turns them into text corrections
	for _, r := range builder.requests {
		if runtime.HasImages(r.Messages) {
			t.Fatal("the CAD builder must not need vision")
		}
	}
	var seen []string
	for _, e := range *events {
		seen = append(seen, e.State)
	}
	got := strings.Join(seen, ">")
	for _, want := range []string{"ANALYZING", "PLANNING", "BUILDING", "RENDERING", "CRITIQUING", "CORRECTING", "ADVANCING", "COMPLETED"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing state %s in %s", want, got)
		}
	}
}

func TestCorrectionThatWorsensIsRevertedAndNotRepeated(t *testing.T) {
	c, a, _, events := newRun(t, 3000) // the "fix" makes the body even taller
	im, _ := a.Images.Put(sideRef(t))
	var reverted []string
	c.OnRevert = func(ids []string) { reverted = append(reverted, ids...) }
	res, err := c.Run(context.Background(), "a simple box car", []string{im.ID})
	if err != nil {
		t.Fatal(err)
	}
	vs := a.Visual
	if res.State != StateStopped || len(reverted) != 3 || !vs.History[1].Reverted || len(vs.Failed) != 1 {
		t.Fatalf("state=%s reverted=%v failed=%v summary=%s", res.State, reverted, vs.Failed, res.Summary)
	}
	if p := vs.History[1].ParametersAfter["vehicle.height"]; p != 3000 {
		t.Fatalf("the reverted iteration still records what was tried: %v", p)
	}
	m, _ := a.Model()
	if m.Nodes["body"].Body.Props.MaxZ != 1600 {
		t.Fatal("the model must be back to its state before the bad correction")
	}
	var revertedEvent bool
	for _, e := range *events {
		revertedEvent = revertedEvent || e.State == StateReverting
	}
	if !revertedEvent || !strings.Contains(res.Summary, "No actionable corrections") {
		t.Fatalf("summary: %s", res.Summary)
	}
}
