package ui

import (
	"encoding/json"
	"testing"

	geo "github.com/accelone/simoncad/internal/agent/capabilities/geometry"
	vis "github.com/accelone/simoncad/internal/agent/capabilities/vision"
	visualcap "github.com/accelone/simoncad/internal/agent/capabilities/visual"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/vision"
)

func selected(s *Service, images []string) map[string]bool {
	out := map[string]bool{}
	for _, c := range runtime.SelectCaps(s.app, images, geo.New(), vis.New(), visualcap.New()) {
		out[c.Name()] = true
	}
	return out
}

// A plain modeling project pays for the geometry catalog only: the image and
// reference-driven tool schemas are the bulk of the input and have nothing to do here.
func TestEmptyProjectLoadsGeometryOnly(t *testing.T) {
	s := newSvc(t)
	got := selected(s, nil)
	if !got["geometry"] || got["vision"] || got["visual"] {
		t.Fatalf("capabilities = %v, want geometry only", got)
	}
}

func TestAttachingAnImageLoadsTheImageCapabilities(t *testing.T) {
	s := newSvc(t)
	got := selected(s, []string{"img_1"})
	if !got["geometry"] || !got["vision"] || !got["visual"] {
		t.Fatalf("capabilities = %v, want all three", got)
	}
}

// An image uploaded earlier that nobody interpreted is no reason to carry the image tools: a
// later turn about a material or a fillet must not pay for them.
func TestAnUploadedImageAloneDoesNotKeepThemLoaded(t *testing.T) {
	s := newSvc(t)
	if _, err := s.UploadImage(testPNG(t)); err != nil {
		t.Fatal(err)
	}
	if got := selected(s, nil); got["vision"] || got["visual"] {
		t.Fatalf("capabilities = %v, want geometry only", got)
	}
}

// An interpretation still waiting keeps the image tools available, so "make that hole 5 mm"
// is answered with the tools it needs; closing it (discarded, or the model was built another
// way) drops them again.
func TestOpenInterpretationKeepsVisionLoadedUntilClosed(t *testing.T) {
	s := newSvc(t)
	it := s.app.Vision.Add(&vision.Proposal{})
	if got := selected(s, nil); !got["vision"] {
		t.Fatalf("capabilities = %v, want vision kept", got)
	}
	if err := s.CloseInterpretation(it.ID); err != nil {
		t.Fatal(err)
	}
	if got := selected(s, nil); got["vision"] {
		t.Fatalf("capabilities = %v, want vision dropped once closed", got)
	}
	if err := s.CloseInterpretation("int_9999"); err == nil {
		t.Fatal("closing an unknown interpretation must fail")
	}
}

// Design parameters are the state visual refinement works from, so their presence alone
// keeps that capability loaded even in a project with no image.
func TestDesignParametersKeepVisualLoaded(t *testing.T) {
	s := newSvc(t)
	o, err := s.CallTool("define_parameters", json.RawMessage(
		`{"set":"plate","parameters":{"width":100,"height":60},"intent":"Plate parameters"}`))
	if err != nil {
		t.Fatal(err)
	}
	if o.Error != "" {
		t.Fatalf("define_parameters: %s", o.Error)
	}
	st, err := s.State() // Review level proposes; the parameters land on commit
	if err != nil {
		t.Fatal(err)
	}
	for _, cs := range st.Pending {
		if _, err := s.Commit(cs.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := selected(s, nil); !got["visual"] {
		t.Fatalf("capabilities = %v, want visual kept", got)
	}
}

// The agent of a plain project must not be offered the image tools at all.
func TestToolsetOfAPlainProjectExcludesImageTools(t *testing.T) {
	s := newSvc(t)
	rt := &runtime.Runtime{Caps: runtime.SelectCaps(s.app, nil, geo.New(), vis.New(), visualcap.New())}
	names := map[string]bool{}
	for _, c := range rt.Caps {
		for _, tl := range c.ToolSubset(s.app.Reg) {
			names[tl.Name] = true
		}
	}
	if !names["create_box"] {
		t.Fatal("geometry tools are missing")
	}
	for _, n := range []string{"analyze_image", "create_from_image", "render_model"} {
		if names[n] {
			t.Fatalf("%s is still offered to a project with no image", n)
		}
	}
}
