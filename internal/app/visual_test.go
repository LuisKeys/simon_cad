package app

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustTool(t *testing.T, a *App, name, args string) Outcome {
	t.Helper()
	o := a.ApplyTool(name, []byte(args), PolicyCommit)
	if !o.OK {
		t.Fatalf("%s %s: %s", name, args, o.Error)
	}
	return o
}

func TestDesignParametersDriveGeometryAndRegionsLock(t *testing.T) {
	a := New("v", time.Now)
	defer a.Close()
	mustTool(t, a, "define_parameters", `{"set":"vehicle","parameters":{"length":4000,"height":1100,"width":1800}}`)
	mustTool(t, a, "create_box", `{"id":"body","dx":100,"dy":100,"dz":100}`)
	mustTool(t, a, "bind_parameters", `{"feature":"body","bindings":{"dx":"vehicle.length","dy":"vehicle.width","dz":"vehicle.height"}}`)
	m, _ := a.Model()
	if p := m.Nodes["body"].Body.Props; math.Abs(p.MaxX-4000) > 1e-6 || math.Abs(p.MaxZ-1100) > 1e-6 {
		t.Fatalf("bound box: %+v", p)
	}
	if len(m.Leaves) != 1 || m.Nodes["vehicle"].Status != "ok" {
		t.Fatal("a parameter set is not a body")
	}
	// one parameter edit regenerates the dependent feature
	mustTool(t, a, "set_parameter", `{"name":"height","value":1000}`)
	m, _ = a.Model()
	if math.Abs(m.Nodes["body"].Body.Props.MaxZ-1000) > 1e-6 {
		t.Fatal("set_parameter must regenerate dependents")
	}
	if o := a.ApplyTool("set_parameter", []byte(`{"name":"roof","value":1}`), PolicyCommit); o.OK || !strings.Contains(o.Error, "known: vehicle.height") {
		t.Fatalf("unknown parameter: %s", o.Error)
	}

	// offscreen renders: fixed frame, silhouette proportions follow the geometry
	rs, err := a.Render([]string{"left", "front"}, 300, 300, "silhouette")
	if err != nil {
		t.Fatal(err)
	}
	x0, y0, x1, y1, _ := rs[0].Mask.BBox()
	if ar := float64(y1-y0) / float64(x1-x0); math.Abs(ar-0.25) > 0.02 {
		t.Fatalf("left silhouette aspect %.3f, want 0.25", ar)
	}
	if r, ok := a.Visual.Render(rs[1].ID); !ok || len(r.PNG) == 0 {
		t.Fatal("renders are kept for the agent and the UI")
	}

	// regions and locks
	mustTool(t, a, "define_region", `{"name":"body","objects":["body"],"parameters":["vehicle.height"]}`)
	mustTool(t, a, "lock_region", `{"name":"body"}`)
	for _, c := range []struct{ tool, args string }{
		{"set_parameter", `{"name":"height","value":900}`},
		{"modify_parameter", `{"feature":"body","parameter":"x","value":5}`},
		{"add_hole", `{"target":"body","diameter":50,"positions":[[2000,900]]}`},
		{"delete_objects", `{"features":["body"]}`},
	} {
		o := a.ApplyTool(c.tool, []byte(c.args), PolicyCommit)
		if o.OK || !strings.Contains(o.Error, "region locked") {
			t.Fatalf("%s must be refused on a locked region: %+v", c.tool, o.Error)
		}
	}
	mustTool(t, a, "set_parameter", `{"name":"length","value":4100}`) // not owned by the region
	mustTool(t, a, "set_color", `{"features":["body"],"color":"#CC0000"}`) // appearance is allowed
	mustTool(t, a, "unlock_region", `{"name":"body"}`)
	mustTool(t, a, "set_parameter", `{"name":"height","value":900}`)

	// calibration and persistence
	im, _ := a.Images.Put(testPNGBytes(t))
	mustTool(t, a, "calibrate_reference_image", `{"image_id":"`+im.ID+`","view":"left","azimuth":90,"elevation":2}`)
	o := mustTool(t, a, "get_visual_state", `{}`)
	b, _ := json.Marshal(o.Data)
	if !strings.Contains(string(b), `"manual":true`) || !strings.Contains(string(b), `"vehicle.length":4100`) {
		t.Fatalf("visual state: %s", b)
	}
	a.Visual.Lock()
	a.Visual.SetLocked("body", true)
	a.Visual.Unlock()
	dir := filepath.Join(t.TempDir(), "v.scadproj")
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	back, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	if r := back.Visual.Region("body"); r == nil || !r.Locked || back.Visual.Reference(im.ID) == nil || back.Visual.Reference(im.ID).Calibration.Azimuth != 90 {
		t.Fatalf("visual state must persist: %+v", back.Visual.Regions)
	}
	if o := back.ApplyTool("set_parameter", []byte(`{"name":"height","value":800}`), PolicyCommit); o.OK {
		t.Fatal("locks survive reopening")
	}
}

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 32))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
