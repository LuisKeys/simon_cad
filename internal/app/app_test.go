package app

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func clock() time.Time { return fixedNow }

func mustOK(t *testing.T, o Outcome) Outcome {
	t.Helper()
	if !o.OK {
		t.Fatalf("tool %s failed: %s", o.Tool, o.Error)
	}
	return o
}

func vol(t *testing.T, a *App) float64 {
	t.Helper()
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	return m.Volume()
}

func near(a, b float64) bool { return math.Abs(a-b)/math.Max(1, math.Abs(b)) < 1e-6 }

// The M1 flow without an LLM: the three target interactions as direct tool calls.
func TestTargetInteractionsViaTools(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()

	o := mustOK(t, a.ApplyTool("create_box", []byte(`{"name":"plate","dx":100,"dy":60,"dz":4,"intent":"Base plate"}`), PolicyAuto))
	if !o.Committed || o.ChangeSet.ID != "cs_0001" {
		t.Fatalf("single-feature create should auto-commit: %+v", o)
	}
	if !near(vol(t, a), 100*60*4) {
		t.Fatal("plate volume")
	}
	pl := a.Graph.Get("plate")
	if pl == nil || pl.Provenance.CreatedBy != "cli" || pl.Provenance.ChangeSet != "cs_0001" || pl.Provenance.ToolCallID != "tc_0001" || pl.Intent != "Base plate" {
		t.Fatalf("provenance/intent not recorded: %+v", pl)
	}

	mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"plate","diameter":3.2,"positions":[[5,5],[95,5],[5,55],[95,55]]}`), PolicyAuto))
	want := 100.0*60*4 - 4*math.Pi*1.6*1.6*4
	if !near(vol(t, a), want) {
		t.Fatalf("volume after holes %v want %v", vol(t, a), want)
	}

	mustOK(t, a.ApplyTool("modify_parameter", []byte(`{"feature":"plate","parameter":"dz","value":5}`), PolicyAuto))
	want = 100.0*60*5 - 4*math.Pi*1.6*1.6*5
	if !near(vol(t, a), want) {
		t.Fatalf("volume after thickness change %v want %v", vol(t, a), want)
	}
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if !near(vol(t, a), 100.0*60*4-4*math.Pi*1.6*1.6*4) {
		t.Fatal("undo did not restore thickness")
	}
}

func TestPolicyAndValidationFailures(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":50,"dy":50,"dz":4}`), PolicyAuto))

	// Preview policy leaves a pending ChangeSet that has not touched the model.
	o := mustOK(t, a.ApplyTool("create_cylinder", []byte(`{"id":"post","radius":3,"height":10}`), PolicyPreview))
	if o.Committed || a.Graph.Get("post") != nil || len(a.Pending()) != 1 {
		t.Fatal("preview must not commit")
	}
	if _, err := a.Commit(o.ChangeSet.ID); err != nil || a.Graph.Get("post") == nil {
		t.Fatalf("explicit commit failed: %v", err)
	}

	// Multi-command ChangeSets are not auto-committed.
	mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"plate","diameter":3,"positions":[[10,10]]}`), PolicyAuto))
	o = mustOK(t, a.ApplyTool("modify_parameter", []byte(`{"feature":"hole","parameter":"diameter","value":4}`), PolicyAuto))
	if !o.Committed {
		t.Fatal("single command should auto-commit")
	}
	mustOK(t, a.ApplyTool("set_expression", []byte(`{"feature":"hole","parameter":"depth","expression":"plate.dz*0.5"}`), PolicyAuto))
	o = mustOK(t, a.ApplyTool("modify_parameter", []byte(`{"feature":"hole","parameter":"depth","value":1}`), PolicyAuto))
	if o.Committed || len(o.ChangeSet.Commands) != 2 {
		t.Fatalf("clearing an expression + setting a value is a 2-command ChangeSet that previews: %+v", o.ChangeSet)
	}

	// Rejections carry actionable messages and never mutate.
	hash := a.Graph.Hash()
	cases := map[string]string{
		`{"tool":"add_hole","args":{"target":"plate","diameter":60}}`:                              "does not fit",
		`{"tool":"add_hole","args":{"target":"plate","diameter":3,"positions":[[0.5,5]]}}`:         "break the edge",
		`{"tool":"add_hole","args":{"target":"ghost","diameter":3}}`:                               "does not exist",
		`{"tool":"create_box","args":{"dx":-1,"dy":1,"dz":1}}`:                                     "must be > 0",
		`{"tool":"create_box","args":{"dx":1,"dy":1,"dz":1,"bogus":1}}`:                            "not a recognized",
		`{"tool":"fillet","args":{"target":"plate","radius":3}}`:                                   "too large",
		`{"tool":"modify_parameter","args":{"feature":"plate","parameter":"nope","value":1}}`:      "no editable parameter",
		`{"tool":"set_expression","args":{"feature":"plate","parameter":"dz","expression":"1 +"}}`: "expression",
		`{"tool":"shell_everything","args":{}}`:                                                    "unknown tool",
		`{"tool":"remove_feature","args":{"feature":"plate"}}`:                                     "used by",
	}
	for c, frag := range cases {
		tool := c[len(`{"tool":"`):strings.Index(c, `","args":`)]
		args := c[strings.Index(c, `"args":`)+len(`"args":`) : len(c)-1]
		o := a.ApplyTool(tool, []byte(args), PolicyCommit)
		if o.OK || !strings.Contains(o.Error, frag) {
			t.Errorf("%s: want error containing %q, got ok=%v err=%q", tool, frag, o.OK, o.Error)
		}
	}
	if a.Graph.Hash() != hash {
		t.Fatal("rejected calls mutated the model")
	}
}

func TestSaveLoadRebuildByteIdentical(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	a.SetActor(Actor{CreatedBy: "agent", Capability: "geometry", Model: "recorded"})
	a.RecordPrompt("Create a plate with holes")
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":80,"dy":60,"dz":4}`), PolicyAuto))
	mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"plate","diameter":3.2,"positions":[[10,10],[70,10]]}`), PolicyAuto))
	a.Checkpoint("Created plate with mounting holes", false)
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	first := snapshot(t, dir)
	a.Close()

	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Model(); err != nil {
		t.Fatal(err)
	}
	dir2 := filepath.Join(t.TempDir(), "q.scadproj")
	if err := b.Save(dir2); err != nil {
		t.Fatal(err)
	}
	second := snapshot(t, dir2)
	for name, content := range first {
		if second[name] != content {
			t.Errorf("%s differs after load→rebuild→save", name)
		}
	}
	if len(second) != len(first) {
		t.Errorf("file sets differ: %d vs %d", len(first), len(second))
	}

	// Replay from the recorded ChangeSets reproduces the model exactly.
	_, match, n, err := b.Replay()
	if err != nil || !match || n != 2 {
		t.Fatalf("replay: match=%v n=%d err=%v", match, n, err)
	}

	// Every mutation traces to a validated tool call in agent-history.json.
	for _, cs := range b.Mgr.Log {
		if cs.State != "committed" || cs.Provenance.ToolCallID == "" {
			t.Errorf("changeset %s lacks tool-call provenance", cs.ID)
		}
		found := false
		for _, tc := range b.AH.ToolCalls {
			if tc.ID == cs.Provenance.ToolCallID && tc.ChangeSetID == cs.ID && tc.Status == "ok" {
				found = true
			}
		}
		if !found {
			t.Errorf("changeset %s has no matching tool call", cs.ID)
		}
	}
	b.Close()
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func TestCheckpointRestoreAndExport(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":40,"dy":40,"dz":4}`), PolicyAuto))
	a.Checkpoint("Created plate", false)
	mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"plate","diameter":5}`), PolicyAuto))
	a.Checkpoint("Added center hole", false)
	if near(vol(t, a), 40*40*4) {
		t.Fatal("hole should reduce volume")
	}
	o := mustOK(t, a.Restore("Created plate", PolicyCommit))
	if !o.Committed || a.Graph.Get("hole") != nil || !near(vol(t, a), 40*40*4) {
		t.Fatal("restore failed")
	}
	if _, err := a.Undo(); err != nil || a.Graph.Get("hole") == nil {
		t.Fatalf("restore must be undoable: %v", err)
	}

	dir := t.TempDir()
	step := mustOK(t, a.ApplyTool("export_step", []byte(`{"path":"`+filepath.Join(dir, "p.step")+`"}`), PolicyAuto))
	if r := step.Data.(map[string]any)["reimport_volume_rel_error"].(float64); r > 1e-6 {
		t.Fatalf("step reimport error %v", r)
	}
	stl := mustOK(t, a.ApplyTool("export_stl", []byte(`{"path":"`+filepath.Join(dir, "p.stl")+`"}`), PolicyAuto))
	if !stl.Data.(map[string]any)["watertight"].(bool) {
		t.Fatal("stl not watertight")
	}
}

func TestSelectionContextAndMeasure(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":40,"dy":30,"dz":4}`), PolicyAuto))
	if err := a.SetSelection([]string{"face(feature=plate, side=+Z)"}); err != nil {
		t.Fatal(err)
	}
	ctx := a.SelectionContext()
	e := ctx.Entities[0]
	if e.Selector != "face(feature=plate,side=+Z)" || e.Area != 1200 || e.Centroid != [3]float64{20, 15, 4} || e.Normal == nil || e.Normal[2] != 1 {
		t.Fatalf("descriptor %+v", e)
	}
	if err := a.SetSelection([]string{"face(feature=ghost,side=+Z)"}); err == nil {
		t.Fatal("unresolvable selection must be rejected")
	}
	o := mustOK(t, a.ApplyTool("measure_distance", []byte(`{"from":"face(feature=plate,side=-Z)","to":"face(feature=plate,side=+Z)"}`), PolicyAuto))
	if d := o.Data.(map[string]any)["distance_mm"].(float64); d != 4 {
		t.Fatalf("thickness measure %v", d)
	}
	// The selection becomes provenance on features created while it is active.
	mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"plate","diameter":3}`), PolicyAuto))
	if refs := a.Graph.Get("hole").SelectionRefs; len(refs) != 1 || refs[0] != "face(feature=plate,side=+Z)" {
		t.Fatalf("selection_refs %v", refs)
	}
}

func TestNoKernelLeaksAfterClose(t *testing.T) {
	a := New("leak", clock)
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"p","dx":50,"dy":50,"dz":4}`), PolicyAuto))
	for i := 0; i < 5; i++ {
		mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"p","diameter":3,"positions":[[10,10]]}`), PolicyPreview))
	}
	mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"p","diameter":3,"positions":[[10,10]]}`), PolicyAuto))
	a.Undo()
	a.Redo()
	a.Engine.Close()
	if n := a.K.LiveShapes(); n != 0 {
		t.Fatalf("leaked %d kernel shapes", n)
	}
	a.K.Close()
}

// Performance budget: a parameter edit on a 50-feature part rebuilds in < 300 ms.
func TestIncrementalRebuildBudget(t *testing.T) {
	a := New("perf", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"base","dx":400,"dy":400,"dz":6}`), PolicyAuto))
	prev := "base"
	for i := 0; i < 49; i++ {
		id := fmt.Sprintf("h%02d", i)
		args := fmt.Sprintf(`{"id":"%s","target":"%s","diameter":4,"positions":[[%d,%d]]}`, id, prev, 20+(i%10)*38, 20+(i/10)*38)
		mustOK(t, a.ApplyTool("add_hole", []byte(args), PolicyCommit))
		prev = id
	}
	if len(a.Graph.Nodes) != 50 {
		t.Fatalf("nodes %d", len(a.Graph.Nodes))
	}
	start := time.Now()
	// Editing the LAST feature only recomputes that node.
	mustOK(t, a.ApplyTool("modify_parameter", []byte(`{"feature":"h48","parameter":"diameter","value":5}`), PolicyCommit))
	dt := time.Since(start)
	t.Logf("incremental edit of 1/50 features (incl. validation + commit): %v; cache %+v", dt, a.Engine.Stats)
	if dt > 300*time.Millisecond {
		t.Errorf("incremental rebuild took %v (budget 300ms)", dt)
	}
}
