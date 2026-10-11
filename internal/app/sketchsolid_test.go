package app

import (
	"fmt"
	"testing"
)

// approve applies a tool whose change is proposed for review (an interpreted drawing) and commits it.
func approve(t *testing.T, a *App, tool, args string) Outcome {
	t.Helper()
	o := a.ApplyTool(tool, []byte(args), PolicyCommit)
	if !o.OK {
		t.Fatalf("%s: %s", tool, o.Error)
	}
	if o.Committed {
		t.Fatalf("%s committed without review", tool)
	}
	if _, err := a.Commit(o.ChangeSet.ID); err != nil {
		t.Fatal(err)
	}
	return o
}

// 8. a drawing becomes a solid: volume = traced area x depth, checked by the critic
func TestSketchToSolidFromADrawing(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := glyphPaper(t)
	o := approve(t, a, "sketch_to_solid", fmt.Sprintf(`{"id":"bracket","path":%q,"width_mm":30,"depth":5}`, img))
	if o.ChangeSet.Explanation == "" {
		t.Error("no explanation")
	}
	area := glyphAreaPx / 100.0 // 10 px per mm
	within(t, "volume", vol(t, a), area*5, 0.02)
	assertWatertight(t, a, "bracket")
	r := resultMap(t, sw(t, a, "check_solid_vs_trace", `{"solid":"bracket","tolerance":0.03}`))
	if r["consistent"] != true {
		t.Fatalf("critic: %v", r)
	}
	// the group is editable as one: change the depth and the critic follows the new promise
	sw(t, a, "modify_parameter", `{"feature":"bracket","parameter":"depth","value":8}`)
	within(t, "taller", vol(t, a), area*8, 0.02)
	if r := resultMap(t, sw(t, a, "check_solid_vs_trace", `{"solid":"bracket","tolerance":0.03}`)); r["consistent"] != true {
		t.Fatalf("critic after edit: %v", r)
	}
}

// the critic only judges what sketch_to_solid made
func TestCriticOnlyJudgesTracedSolids(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	sw(t, a, "create_sketch", `{"id":"sk","plane":"XY","entities":[{"id":"r","kind":"rectangle","x":0,"y":0,"w":10,"h":10}]}`)
	sw(t, a, "extrude_sketch", `{"id":"plain","sketch":"sk","depth":3}`)
	if o := a.ApplyTool("check_solid_vs_trace", []byte(`{"solid":"plain"}`), PolicyCommit); o.OK {
		t.Error("the critic judged a solid that has no trace")
	}
	if o := a.ApplyTool("check_solid_vs_trace", []byte(`{"solid":"sk"}`), PolicyCommit); o.OK {
		t.Error("the critic judged a sketch")
	}
}

func TestSketchToSolidStrokes(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	approve(t, a, "sketch_to_solid", fmt.Sprintf(`{"id":"lines","path":%q,"width_mm":30,"style":"strokes","stroke_width":1.5,"depth":2}`, glyphPaper(t)))
	if v := vol(t, a); v <= 0 || v > 30*40*2 {
		t.Errorf("volume %v", v)
	}
	assertWatertight(t, a, "lines")
}

// nothing is invented: the size, the depth and the stroke width must be given
func TestSketchToSolidInventsNothing(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	img := glyphPaper(t)
	h := a.Graph.Hash()
	if c := failCode(t, a, "sketch_to_solid", fmt.Sprintf(`{"path":%q,"depth":5}`, img)); c != "SCALE_REQUIRED" {
		t.Errorf("code %q", c)
	}
	for _, bad := range []string{
		fmt.Sprintf(`{"path":%q,"width_mm":30}`, img),                             // no depth
		fmt.Sprintf(`{"path":%q,"width_mm":30,"depth":5,"style":"strokes"}`, img), // strokes need a width
		fmt.Sprintf(`{"path":%q,"width_mm":30,"depth":5,"stroke_width":1}`, img),  // silhouette takes none
		fmt.Sprintf(`{"path":%q,"width_mm":30,"depth":-1}`, img),
		`{"width_mm":30,"depth":5}`,
	} {
		if o := a.ApplyTool("sketch_to_solid", []byte(bad), PolicyCommit); o.OK {
			t.Errorf("%s accepted", bad)
		}
	}
	if a.Graph.Hash() != h {
		t.Error("a failed call changed the graph")
	}
}
