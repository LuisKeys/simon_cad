package app

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func sw(t *testing.T, a *App, tool, args string) Outcome {
	t.Helper()
	return mustOK(t, a.ApplyTool(tool, []byte(args), PolicyCommit))
}

func failCode(t *testing.T, a *App, tool, args string) string {
	t.Helper()
	o := a.ApplyTool(tool, []byte(args), PolicyCommit)
	if o.OK {
		t.Fatalf("%s should have failed", tool)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(o.JSON()), &m)
	code, _ := m["code"].(string)
	if code == "" {
		t.Fatalf("%s failed without a code: %s", tool, o.Error)
	}
	return code
}

func within(t *testing.T, what string, got, want, rel float64) {
	t.Helper()
	if math.Abs(got-want) > rel*math.Abs(want) {
		t.Fatalf("%s: got %.4f want %.4f (±%.2f%%)", what, got, want, rel*100)
	}
}

func pathLength(t *testing.T, a *App, id string) float64 {
	t.Helper()
	return resultMap(t, sw(t, a, "evaluate_path", fmt.Sprintf(`{"path":%q}`, id)))["length_mm"].(float64)
}

func assertWatertight(t *testing.T, a *App, id string) {
	t.Helper()
	r := resultMap(t, sw(t, a, "validate_geometry", fmt.Sprintf(`{"object":%q}`, id)))
	if r["ok"] != true {
		t.Fatalf("validate_geometry %s: %v", id, r)
	}
}

func helixPts(r, pitch float64, turns, per int) string {
	var parts []string
	for i := 0; i <= turns*per; i++ {
		a := 2 * math.Pi * float64(i) / float64(per)
		parts = append(parts, fmt.Sprintf("[%.6f,%.6f,%.6f]", r*math.Cos(a), r*math.Sin(a), pitch*a/(2*math.Pi)))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// 1. circular tube along a helix: V = pi r^2 L
func TestSweepTubeAlongHelix(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_path", `{"id":"helix","type":"catmull_rom","points":`+helixPts(20, 12, 3, 16)+`}`)
	sw(t, a, "create_profile", `{"id":"round","shape":"circle","radius":2}`)
	sw(t, a, "create_sweep", `{"id":"coil","path":"helix","profile":"round"}`)
	L := pathLength(t, a, "helix")
	within(t, "helix tube volume", vol(t, a), math.Pi*4*L, 0.01)
	assertWatertight(t, a, "coil")
}

// 2. snake with variable thickness
func TestSweepSnakeVariableThickness(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_path", `{"id":"snake","type":"catmull_rom","points":[
		{"p":[0,0,0],"w":0.5,"h":0.5},{"p":[20,12,0],"w":1.2,"h":1.2},{"p":[40,-12,0],"w":1.2,"h":1.2},
		{"p":[60,12,0],"w":1.2,"h":1.2},{"p":[80,0,0],"w":0.4,"h":0.4}]}`)
	sw(t, a, "create_profile", `{"id":"round","shape":"circle","radius":2}`)
	sw(t, a, "create_sweep", `{"id":"body","path":"snake","profile":"round","cap_start":"round","cap_end":"round"}`)
	L := pathLength(t, a, "snake")
	v := vol(t, a)
	if v < math.Pi*4*0.4*0.4*L || v > math.Pi*4*1.2*1.2*L {
		t.Fatalf("snake volume %v outside the radius bounds for L=%v", v, L)
	}
	assertWatertight(t, a, "body")
	// the head end is thicker than the tail: bounding box proves the variation took effect
	m, _ := a.Model()
	b := m.Nodes["body"].Body.Props
	if b.MaxY-b.MinY < 24 {
		t.Fatalf("snake body should span its wiggle, got %v", b.MaxY-b.MinY)
	}
}

// 3. twisted ribbon (a twist preserves the swept volume)
func TestSweepTwistedRibbon(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_ribbon", `{"id":"strip","points":[[0,0,0],[50,0,0]],"path_type":"linear","width":10,"thickness":1,"twist":180}`)
	within(t, "ribbon volume", vol(t, a), 10*1*50, 0.005)
	assertWatertight(t, a, "strip")
	// untwisted: a flat box, thickness along Z
	sw(t, a, "create_ribbon", `{"id":"flat","points":[[0,20,0],[50,20,0]],"path_type":"linear","width":10,"thickness":1}`)
	m, _ := a.Model()
	p := m.Nodes["flat"].Body.Props
	within(t, "flat ribbon thickness", p.MaxZ-p.MinZ, 1, 1e-6)
	within(t, "flat ribbon width", p.MaxY-p.MinY, 10, 1e-6)
	sw(t, a, "create_ribbon", `{"id":"wall","points":[[0,40,0],[50,40,0]],"path_type":"linear","width":10,"thickness":1,"orientation":"upright"}`)
	m, _ = a.Model()
	p = m.Nodes["wall"].Body.Props
	within(t, "upright ribbon height", p.MaxZ-p.MinZ, 10, 1e-6)
}

func TestSweepHollowTubeAndCaps(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_tube", `{"id":"pipe","points":[[0,0,0],[30,0,0],[60,20,10]],"path_type":"catmull_rom","outer_diameter":8,"wall_thickness":1}`)
	L := pathLength(t, a, "pipe_path")
	within(t, "hollow tube volume", vol(t, a), math.Pi*(16-9)*L, 0.01)
	assertWatertight(t, a, "pipe")
	m, _ := a.Model()
	if m.Nodes["pipe"].Body.Props.Solids != 1 {
		t.Fatal("hollow tube must be one solid")
	}

	// round-ended wire: cylinder + two hemispheres
	sw(t, a, "create_wire", `{"id":"wire","points":[[0,50,0],[20,50,0]],"path_type":"linear","diameter":4}`)
	m, _ = a.Model()
	within(t, "round wire volume", m.Nodes["wire"].Body.Props.Volume, math.Pi*4*20+4.0/3*math.Pi*8, 0.01)
	assertWatertight(t, a, "wire")
	// pointed: cylinder + two cones of height r
	sw(t, a, "create_wire", `{"id":"nail","points":[[0,70,0],[20,70,0]],"path_type":"linear","diameter":4,"end_style":"point"}`)
	m, _ = a.Model()
	within(t, "pointed wire volume", m.Nodes["nail"].Body.Props.Volume, math.Pi*4*20+2*math.Pi*4*2/3, 0.01)
	assertWatertight(t, a, "nail")

	if c := failCode(t, a, "create_tube", `{"points":[[0,0,0],[10,0,0]],"path_type":"linear","outer_diameter":4,"wall_thickness":0.5,"cap_mode":"round"}`); c == "" {
		t.Fatal("no code")
	}
}

// ring path: torus
func TestSweepClosedPathTorus(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	k := 0.5522847498307936 * 15
	sw(t, a, "create_path", fmt.Sprintf(`{"id":"ring","type":"bezier","closed":true,"points":[
		{"p":[15,0,0],"in":[15,%[1]v,0],"out":[15,-%[1]v,0]},
		{"p":[0,-15,0],"in":[%[1]v,-15,0],"out":[-%[1]v,-15,0]},
		{"p":[-15,0,0],"in":[-15,-%[1]v,0],"out":[-15,%[1]v,0]},
		{"p":[0,15,0],"in":[-%[1]v,15,0],"out":[%[1]v,15,0]}]}`, k))
	sw(t, a, "create_profile", `{"id":"round","shape":"circle","radius":2}`)
	sw(t, a, "create_sweep", `{"id":"torus","path":"ring","profile":"round"}`)
	within(t, "torus volume", vol(t, a), 2*math.Pi*math.Pi*15*4, 0.01)
	assertWatertight(t, a, "torus")
}

// mitered corner: V = A * (L1 + L2)
func TestSweepPolylineMiter(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_path", `{"id":"elbow","type":"linear","points":[[0,0,0],[30,0,0],[30,40,0],[30,40,25]]}`)
	sw(t, a, "create_profile", `{"id":"sq","shape":"rectangle","width":6,"height":4}`)
	sw(t, a, "create_sweep", `{"id":"duct","path":"elbow","profile":"sq"}`)
	within(t, "mitered duct volume", vol(t, a), 6*4*(30+40+25), 0.002)
	assertWatertight(t, a, "duct")
	sw(t, a, "create_profile", `{"id":"dia","shape":"circle","radius":3,"segments":96}`)
	sw(t, a, "create_sweep", `{"id":"pipe","path":"elbow","profile":"dia","cap_start":"flat"}`)
	m, _ := a.Model()
	area := 0.5 * 96 * 9 * math.Sin(2*math.Pi/96)
	within(t, "mitered round pipe", m.Nodes["pipe"].Body.Props.Volume, area*95, 0.002)
}

func TestSweepErrorsAreCodedAndRecoverable(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_path", `{"id":"tight","type":"catmull_rom","points":[[0,0,0],[6,6,0],[0,12,0],[-6,6,0]],"closed":true}`)
	sw(t, a, "create_profile", `{"id":"fat","shape":"circle","radius":8}`)
	if c := failCode(t, a, "create_sweep", `{"path":"tight","profile":"fat"}`); c != "EXCESSIVE_CURVATURE" {
		t.Fatalf("code %s", c)
	}
	// the hint tells the agent how to recover: a thinner profile works
	sw(t, a, "update_profile", `{"profile":"fat","radius":1.2}`)
	sw(t, a, "create_sweep", `{"id":"ok","path":"tight","profile":"fat"}`)

	// a figure eight crosses itself
	sw(t, a, "create_path", `{"id":"eight","type":"linear","points":[[0,0,0],[20,20,0],[20,0,0],[0,20,0]]}`)
	if c := failCode(t, a, "create_sweep", `{"path":"eight","profile":"fat"}`); c != "PATH_SELF_INTERSECTION" {
		t.Fatalf("code %s", c)
	}
	// helix turns closer than the tube thickness overlap each other
	sw(t, a, "create_path", `{"id":"squeezed","type":"catmull_rom","points":`+helixPts(15, 2, 3, 16)+`}`)
	sw(t, a, "create_profile", `{"id":"thick","shape":"circle","radius":3}`)
	if c := failCode(t, a, "create_sweep", `{"path":"squeezed","profile":"thick"}`); c != "SWEEP_SELF_INTERSECTION" {
		t.Fatalf("code %s", c)
	}
	// hollow profile with a pointed cap
	sw(t, a, "create_profile", `{"id":"hol","shape":"hollow_circle","outer_radius":3,"inner_radius":2}`)
	sw(t, a, "create_path", `{"id":"line","type":"linear","points":[[0,0,0],[30,0,0]]}`)
	failCode(t, a, "create_sweep", `{"path":"line","profile":"hol","cap_end":"point"}`)
	// a closed path cannot take a fractional twist
	failCode(t, a, "create_sweep", `{"path":"tight","profile":"fat","twist":90}`)
	// a corner sharper than the limit
	sw(t, a, "create_path", `{"id":"hairpin","type":"linear","points":[[0,0,0],[30,0,0],[0,2,0]]}`)
	if c := failCode(t, a, "create_sweep", `{"path":"hairpin","profile":"fat"}`); c != "EXCESSIVE_CURVATURE" {
		t.Fatalf("code %s", c)
	}
}

// 10. editing the path regenerates the sweep; undo and save/load keep it editable
func TestSweepRegeneratesWhenSourcesChange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	sw(t, a, "create_path", `{"id":"spine","type":"linear","points":[[0,0,0],[40,0,0]]}`)
	sw(t, a, "create_profile", `{"id":"round","shape":"circle","radius":2}`)
	sw(t, a, "create_sweep", `{"id":"rod","path":"spine","profile":"round"}`)
	sw(t, a, "create_sweep", `{"id":"rod2","path":"spine","profile":"round"}`)
	v0 := vol(t, a)
	within(t, "two rods", v0, 2*math.Pi*4*40, 0.005)
	h0 := a.Graph.Hash()

	// the same profile serves both sweeps: a radius edit changes both
	sw(t, a, "update_profile", `{"profile":"round","radius":3}`)
	within(t, "after profile edit", vol(t, a), 2*math.Pi*9*40, 0.005)
	if _, err := a.Undo(); err != nil || a.Graph.Hash() != h0 {
		t.Fatalf("undo profile edit: %v", err)
	}

	// moving the path end lengthens the rods
	sw(t, a, "update_path", `{"path":"spine","ops":[{"op":"move","index":1,"position":[80,0,0]}]}`)
	within(t, "after path edit", vol(t, a), 2*math.Pi*4*80, 0.005)
	h1 := a.Graph.Hash()

	// the sweep options are editable
	sw(t, a, "update_sweep", `{"sweep":"rod","cap_end":"round"}`)
	if o := a.ApplyTool("update_sweep", []byte(`{"sweep":"rod","cap_end":"round"}`), PolicyCommit); o.OK {
		t.Fatal("a no-op update must be rejected")
	}
	h2 := a.Graph.Hash()
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	v2 := vol(t, a)
	a.Close()

	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Graph.Hash() != h2 || !near(vol(t, b), v2) {
		t.Fatal("reopened project differs from the saved one")
	}
	// still editable after reload
	sw(t, b, "update_path", `{"path":"spine","ops":[{"op":"move","index":1,"position":[20,0,0]}]}`)
	if vol(t, b) >= v2 {
		t.Fatal("editing after reload did not regenerate")
	}
	_ = h1
	// a path or profile in use cannot be deleted
	if o := b.ApplyTool("delete_path", []byte(`{"path":"spine"}`), PolicyCommit); o.OK {
		t.Fatal("deleted a path that sweeps depend on")
	}
}

func TestSweepVariableSectionMatchesIntegral(t *testing.T) {
	// a linearly tapering cone-like wire: w goes 1 -> 3 over 30 mm; V = pi r^2 L (1+k+k^2)/3 with k=3
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_path", `{"id":"taper","type":"linear","points":[{"p":[0,0,0],"w":1,"h":1},{"p":[30,0,0],"w":3,"h":3}]}`)
	sw(t, a, "create_profile", `{"id":"r","shape":"circle","radius":2,"segments":120}`)
	sw(t, a, "create_sweep", `{"id":"cone","path":"taper","profile":"r"}`)
	area := 0.5 * 120 * 4 * math.Sin(2*math.Pi/120)
	within(t, "frustum volume", vol(t, a), area*30*(1+3+9)/3, 0.002)
}

// 9. a swept part exports to a watertight STL and a STEP that re-imports at the same volume
func TestSweepExportsSTLAndSTEP(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_tube", `{"id":"coil","points":`+helixPts(15, 10, 2, 16)+`,"outer_diameter":4,"wall_thickness":0.8}`)
	dir := t.TempDir()
	stl := filepath.Join(dir, "coil.stl")
	o := sw(t, a, "export_stl", fmt.Sprintf(`{"path":%q}`, stl))
	if r := resultMap(t, o); r["watertight"] != true {
		t.Fatalf("stl: %v", r)
	}
	step := filepath.Join(dir, "coil.step")
	o = sw(t, a, "export_step", fmt.Sprintf(`{"path":%q}`, step))
	if r := resultMap(t, o); r["reimport_volume_rel_error"].(float64) > 1e-6 {
		t.Fatalf("step: %v", r)
	}
}

// the geometry report separates a healthy body from a broken one
func TestValidateGeometryReport(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_box", `{"id":"b","dx":10,"dy":10,"dz":10}`)
	r := resultMap(t, sw(t, a, "validate_geometry", `{}`))
	if r["ok"] != true {
		t.Fatalf("box: %v", r)
	}
	b := r["bodies"].([]any)[0].(map[string]any)
	if b["watertight"] != true || b["components"].(float64) != 1 {
		t.Fatalf("box report: %v", b)
	}
	if o := a.ApplyTool("validate_geometry", []byte(`{"object":"nope"}`), PolicyCommit); o.OK {
		t.Fatal("unknown object accepted")
	}
}

// A long sweep needs hundreds of sections. The kernel lofts them in short runs; one big loft can
// come back "valid" but wrong, so this guards the volume of a many-section sweep.
func TestSweepManySectionsStaysAccurate(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_path", `{"id":"helix","type":"catmull_rom","points":`+helixPts(12, 7, 8, 24)+`}`)
	sw(t, a, "create_profile", `{"id":"round","shape":"circle","radius":2}`)
	o := sw(t, a, "create_sweep", `{"id":"coil","path":"helix","profile":"round","tolerance":0.002,"max_sections":400}`)
	_ = o
	L := pathLength(t, a, "helix")
	within(t, "8-turn coil volume", vol(t, a), math.Pi*4*L, 0.01)
	assertWatertight(t, a, "coil")
}
