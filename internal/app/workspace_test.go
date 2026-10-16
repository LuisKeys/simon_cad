package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/workspace"
)

// useGlobalWorkspace points the global defaults at s for one test.
func useGlobalWorkspace(t *testing.T, s workspace.Settings) {
	t.Helper()
	old := configWorkspaceDefaults
	configWorkspaceDefaults = func() workspace.Settings { return s.Clone() }
	t.Cleanup(func() { configWorkspaceDefaults = old })
}

func settingsOf(t *testing.T, o Outcome) workspace.Settings {
	t.Helper()
	m, ok := o.Data.(map[string]any)
	if !ok {
		t.Fatalf("workspace tool returned %T", o.Data)
	}
	s, ok := m["settings"].(workspace.Settings)
	if !ok {
		t.Fatalf("no settings in %v", m)
	}
	return s
}

// The agent's example instructions, through the same tools the Settings screen calls.
func TestWorkspaceToolsFollowAgentInstructions(t *testing.T) {
	useGlobalWorkspace(t, workspace.Defaults())
	a := New("ws", clock)
	defer a.Close()
	// "Set the grid spacing to 5 millimeters."
	s := settingsOf(t, mustOK(t, a.ApplyTool("configure_grid", []byte(`{"spacing":5,"unit":"mm"}`), PolicyAuto)))
	if s.Grid.Spacing != 5 || a.Workspace.Grid.Spacing != 5 {
		t.Fatalf("spacing: %v", s.Grid.Spacing)
	}
	// "Switch the project to inches."
	mustOK(t, a.ApplyTool("set_units", []byte(`{"linear":"in"}`), PolicyAuto))
	// "Enable snapping every 10 millimeters."
	mustOK(t, a.ApplyTool("configure_snapping", []byte(`{"enabled":true,"translation":true,"translation_increment":10,"unit":"mm"}`), PolicyAuto))
	// "Set rotation snapping to 45 degrees."
	mustOK(t, a.ApplyTool("configure_snapping", []byte(`{"enabled":true,"rotation":true,"rotation_increment":45}`), PolicyAuto))
	// "Hide the grid."
	mustOK(t, a.ApplyTool("configure_grid", []byte(`{"visible":false}`), PolicyAuto))

	w := a.Workspace
	if w.Units.Linear != workspace.Inch || !w.Snap.Enabled || !w.Snap.Rotation || w.Snap.RotationIncrement != 45 ||
		w.TranslationStep() != 10 || w.Grid.Visible || !w.Grid.Enabled {
		t.Fatalf("after the instructions: %+v", w)
	}
	// Values given in other units are stored canonically.
	mustOK(t, a.ApplyTool("configure_grid", []byte(`{"spacing":0.5,"unit":"in","origin":[1,0,0]}`), PolicyAuto))
	if a.Workspace.Grid.Spacing != 12.7 || a.Workspace.Grid.Origin != [3]float64{25.4, 0, 0} {
		t.Fatalf("inches → mm: %+v", a.Workspace.Grid)
	}
	mustOK(t, a.ApplyTool("configure_snapping", []byte(`{"rotation_increment":0.5,"angle_unit":"rad"}`), PolicyAuto))
	if got := a.Workspace.Snap.RotationIncrement; got < 28.6478 || got > 28.6479 {
		t.Fatalf("0.5 rad = %v°", got)
	}
	mustOK(t, a.ApplyTool("configure_snapping", []byte(`{"translation_increment_from_grid":true}`), PolicyAuto))
	if a.Workspace.Snap.TranslationIncrement != nil || a.Workspace.TranslationStep() != 12.7 {
		t.Fatal("translation increment does not follow the grid again")
	}
	// Reads, queries and restoring defaults.
	got := mustOK(t, a.ApplyTool("get_workspace_settings", nil, PolicyAuto))
	if settingsOf(t, got).Units.Linear != workspace.Inch {
		t.Fatal("get_workspace_settings disagrees with the state")
	}
	q := mustOK(t, a.ApplyTool("snap_calculate", []byte(`{"operation":"angle","angle":100}`), PolicyAuto)).Data.(map[string]any)
	// 0.5 rad steps (28.6479°): 100° is 3.49 steps, so 3 steps = 1.5 rad.
	if q["snapped"] != true || !near(q["angle_rad"].(float64), 1.5) {
		t.Fatalf("snap_calculate angle: %v", q)
	}
	c := mustOK(t, a.ApplyTool("convert_units", []byte(`{"value":2,"from":"in","to":"mm"}`), PolicyAuto)).Data.(map[string]any)
	if c["value"].(float64) != 50.8 {
		t.Fatalf("2 in = %v mm", c["value"])
	}
	mustOK(t, a.ApplyTool("restore_workspace_defaults", []byte(`{"section":"grid"}`), PolicyAuto))
	if a.Workspace.Grid != workspace.Defaults().Grid || a.Workspace.Units.Linear != workspace.Inch {
		t.Fatalf("restoring the grid only: %+v", a.Workspace)
	}
	mustOK(t, a.ApplyTool("restore_workspace_defaults", nil, PolicyAuto))
	if !a.Workspace.Equal(workspace.Defaults()) {
		t.Fatalf("restore all: %+v", a.Workspace)
	}
	// Every call is in the project trace, and each change is an event.
	n := 0
	for _, e := range a.Bus.Events() {
		if e.Type == events.WorkspaceChanged {
			n++
		}
	}
	if n < 8 || len(a.AH.ToolCalls) != 13 {
		t.Fatalf("%d WorkspaceChanged events, %d tool calls", n, len(a.AH.ToolCalls))
	}
}

// Validation is shared: an invalid value is refused and nothing is changed.
func TestWorkspaceToolsRejectInvalidValues(t *testing.T) {
	useGlobalWorkspace(t, workspace.Defaults())
	a := New("ws", clock)
	defer a.Close()
	for _, bad := range []struct{ tool, args, msg string }{
		{"configure_grid", `{"spacing":0}`, "greater than 0"},
		{"configure_grid", `{"spacing":-2}`, "greater than 0"},
		{"configure_grid", `{"extent":-1}`, "greater than 0"},
		{"configure_grid", `{"spacing":0.001}`, "lines per side"},
		{"configure_grid", `{"plane":"XW"}`, "must be one of"},
		{"configure_grid", `{"unit":"in"}`, "at least one"},
		{"configure_snapping", `{"rotation_increment":0}`, "greater than 0"},
		{"configure_snapping", `{"translation_increment":-1}`, "greater than 0"},
		{"configure_snapping", `{"translation_increment":2,"translation_increment_from_grid":true}`, "not both"},
		{"set_units", `{"linear":"yd"}`, "must be one of"},
		{"set_units", `{"linear_precision":12}`, "<= 8"},
		{"convert_units", `{"value":1,"from":"mm","to":"deg"}`, "length"},
	} {
		o := a.ApplyTool(bad.tool, []byte(bad.args), PolicyAuto)
		if o.OK || !strings.Contains(o.Error, bad.msg) {
			t.Errorf("%s %s: ok=%v error=%q (want %q)", bad.tool, bad.args, o.OK, o.Error, bad.msg)
		}
	}
	if !a.Workspace.Equal(workspace.Defaults()) {
		t.Fatalf("a refused call changed the settings: %+v", a.Workspace)
	}
}

// Switching display units never touches geometry: the model, its hash and the bodies'
// positions stay exactly as they were.
func TestSwitchingUnitsDoesNotModifyGeometry(t *testing.T) {
	useGlobalWorkspace(t, workspace.Defaults())
	a := New("ws", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":100,"dy":60,"dz":4,"x":100}`), PolicyAuto))
	hash, geo, v := a.Graph.Hash(), a.Graph.GeometryHash(), vol(t, a)
	changeSets := len(a.Mgr.Log)
	for _, u := range []string{"cm", "in", "ft", "m", "mm"} {
		mustOK(t, a.ApplyTool("set_units", []byte(`{"linear":"`+u+`","angular":"rad"}`), PolicyAuto))
		mustOK(t, a.ApplyTool("configure_grid", []byte(`{"spacing":2.5,"plane":"YZ"}`), PolicyAuto))
	}
	if a.Graph.Hash() != hash || a.Graph.GeometryHash() != geo || !near(vol(t, a), v) {
		t.Fatal("display units changed the model")
	}
	if len(a.Mgr.Log) != changeSets {
		t.Fatal("workspace settings must not create ChangeSets")
	}
	if x := a.Graph.Get("b").Parameters["x"]; x != 100.0 {
		t.Fatalf("x = %v", x)
	}
	d := dims(t, a, "b")
	wantSize(t, d, 100, 60, 4)
}

// New projects copy the global defaults; changing the defaults later never changes an
// existing project, open or saved; a project from before workspace settings gets the factory
// defaults rather than whatever the user's globals are now.
func TestGlobalDefaultsVersusProjectSettings(t *testing.T) {
	global := workspace.Defaults()
	global.Units.Linear, global.Grid.Spacing = workspace.CM, 20
	useGlobalWorkspace(t, global)
	a := New("ws", clock)
	defer a.Close()
	if !a.Workspace.Equal(global) {
		t.Fatalf("new project did not inherit the globals: %+v", a.Workspace)
	}
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	// The user changes their defaults afterwards.
	later := workspace.Defaults()
	later.Units.Linear = workspace.Foot
	useGlobalWorkspace(t, later)
	if a.Workspace.Units.Linear != workspace.CM {
		t.Fatal("the open project followed the new globals")
	}
	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.Workspace.Equal(global) {
		t.Fatalf("the saved project followed the new globals: %+v", b.Workspace)
	}
	// "Restore defaults" is an explicit request: then the project takes the current globals.
	mustOK(t, b.ApplyTool("restore_workspace_defaults", nil, PolicyAuto))
	if !b.Workspace.Equal(later) {
		t.Fatalf("restore did not take the current globals: %+v", b.Workspace)
	}
	mustOK(t, b.ApplyTool("restore_workspace_defaults", []byte(`{"source":"factory"}`), PolicyAuto))
	if !b.Workspace.Equal(workspace.Defaults()) {
		t.Fatal("factory restore")
	}
	legacy, err := Open(filepath.Join("..", "..", "testdata", "robot", "servo_bracket.scadproj"), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if !legacy.Workspace.Equal(workspace.Defaults()) {
		t.Fatalf("legacy project: %+v", legacy.Workspace)
	}
}

// Settings persist across reloads, and a project without workspace.json still loads, then
// gains the file on its next save.
func TestWorkspacePersistsAcrossReloads(t *testing.T) {
	useGlobalWorkspace(t, workspace.Defaults())
	a := New("ws", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_units", []byte(`{"linear":"in","linear_precision":3}`), PolicyAuto))
	mustOK(t, a.ApplyTool("configure_grid", []byte(`{"plane":"XZ","spacing":2.54,"major_interval":5,"extent":508,"origin":[1,2,3]}`), PolicyAuto))
	mustOK(t, a.ApplyTool("configure_snapping", []byte(`{"enabled":true,"rotation":true,"rotation_increment":30}`), PolicyAuto))
	want := a.Workspace
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.Workspace.Equal(want) {
		t.Fatalf("reloaded %+v, want %+v", b.Workspace, want)
	}
	// Old project: remove the file, as a v3 build would have written it.
	if err := os.Remove(filepath.Join(dir, "workspace.json")); err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	raw, _ := os.ReadFile(filepath.Join(dir, "project.json"))
	_ = json.Unmarshal(raw, &meta)
	meta["schema_version"] = 3
	raw, _ = json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "project.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir, clock)
	if err != nil {
		t.Fatalf("a v3 project without workspace.json must load: %v", err)
	}
	defer c.Close()
	if !c.Workspace.Equal(workspace.Defaults()) || c.Graph.Get("b") == nil {
		t.Fatalf("v3 project: %+v", c.Workspace)
	}
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspace.json")); err != nil {
		t.Fatal("the next save must write workspace.json")
	}
}

// Undo and redo always act on the latest edit, whatever it was: workspace-settings changes are
// undo steps interleaved with the ChangeSets. The ChangeSet-only Undo (agent revert, draft
// discard) refuses to cross a settings change instead of skipping it.
func TestUndoRedoInterleavesWorkspaceAndModelEdits(t *testing.T) {
	useGlobalWorkspace(t, workspace.Defaults())
	a := New("ws", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_units", []byte(`{"linear":"cm"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"c","dx":5,"dy":5,"dz":5,"x":50}`), PolicyAuto))
	mustOK(t, a.ApplyTool("configure_grid", []byte(`{"spacing":2.5}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_units", []byte(`{"linear":"cm"}`), PolicyAuto)) // no change: no step

	type snap struct {
		unit    string
		spacing float64
		nodes   int
	}
	now := func() snap { return snap{a.Workspace.Units.Linear, a.Workspace.Grid.Spacing, len(a.Graph.Nodes)} }
	def := workspace.Defaults()
	want := []snap{
		{def.Units.Linear, def.Grid.Spacing, 0},
		{def.Units.Linear, def.Grid.Spacing, 1},
		{"cm", def.Grid.Spacing, 1},
		{"cm", def.Grid.Spacing, 2},
		{"cm", 2.5, 2},
	}
	if got := now(); got != want[4] {
		t.Fatalf("start: %+v", got)
	}
	if _, err := a.Undo(); err == nil || !strings.Contains(err.Error(), "not a model change") {
		t.Fatalf("ChangeSet-only Undo must refuse a settings step, got %v", err)
	}
	for i := 3; i >= 0; i-- {
		if _, err := a.UndoStep(); err != nil {
			t.Fatal(err)
		}
		if got := now(); got != want[i] {
			t.Fatalf("after undo to step %d: %+v, want %+v", i, got, want[i])
		}
	}
	if a.CanUndo() {
		t.Fatal("everything was undone")
	}
	for i := 1; i <= 4; i++ {
		if _, err := a.RedoStep(); err != nil {
			t.Fatal(err)
		}
		if got := now(); got != want[i] {
			t.Fatalf("after redo to step %d: %+v, want %+v", i, got, want[i])
		}
	}
	if a.CanRedo() {
		t.Fatal("everything was redone")
	}
	// A new edit after an undo drops the redo stack, settings steps included.
	if _, err := a.UndoStep(); err != nil {
		t.Fatal(err)
	}
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"d","dx":5,"dy":5,"dz":5,"x":-50}`), PolicyAuto))
	if a.CanRedo() {
		t.Fatal("a new edit must clear redo")
	}
}
