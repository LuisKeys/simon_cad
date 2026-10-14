package ui

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
)

var clock = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func newSvc(t *testing.T) *Service {
	t.Helper()
	s := NewService(app.New("m2", clock), "")
	s.Clock = clock
	t.Cleanup(s.Close)
	return s
}

func scripted(t *testing.T, calls ...string) ProviderFactory {
	t.Helper()
	var rec runtime.Recording
	for i, c := range calls {
		var in map[string]any
		if err := json.Unmarshal([]byte(c[strings.Index(c, "{"):]), &in); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(in)
		rec.Responses = append(rec.Responses, runtime.Response{StopReason: "tool_use", Content: []runtime.Block{
			{Type: "tool_use", ID: "tu_" + string(rune('1'+i)), Name: strings.TrimSpace(c[:strings.Index(c, "{")]), Input: raw}}})
	}
	rec.Responses = append(rec.Responses, runtime.Response{StopReason: "end_turn", Content: []runtime.Block{{Type: "text", Text: "Done."}}})
	rep := &runtime.Replay{Rec: rec}
	return func() (runtime.Provider, error) { return rep, nil }
}

func waitIdle(t *testing.T, s *Service) {
	t.Helper()
	ch, cancel := s.Hub().Subscribe()
	defer cancel()
	deadline := time.After(10 * time.Second)
	for {
		s.mu.Lock()
		busy := s.busy
		s.mu.Unlock()
		if !busy {
			return
		}
		select {
		case <-ch:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("agent did not finish")
		}
	}
}

// The M2 demo, headless: select a face → "Add four M3 holes here" → preview → commit → undo.
func TestM2SelectPromptPreviewCommitUndo(t *testing.T) {
	s := newSvc(t)
	if _, err := s.CallTool("create_box", json.RawMessage(`{"id":"plate","dx":100,"dy":60,"dz":4,"intent":"Base plate"}`)); err != nil {
		t.Fatal(err)
	}
	// Review level: even a single-feature edit stays proposed until committed.
	st, _ := s.State()
	if len(st.Pending) != 1 {
		t.Fatalf("review level should hold create_box for approval, pending=%d", len(st.Pending))
	}
	if _, err := s.Commit(st.Pending[0].ID); err != nil {
		t.Fatal(err)
	}

	// Click a triangle of the top face → semantic selector → selection context.
	sc, err := s.Scene(nil)
	if err != nil || len(sc.Changed) != 1 {
		t.Fatalf("scene %v", err)
	}
	body := sc.Changed[0]
	top := -1
	for _, f := range body.Faces {
		if f.Normal != nil && f.Normal[2] == 1 {
			top = f.TriStart
		}
	}
	sv, err := s.Pick(PickRequest{Body: "plate", Kind: "face", Triangle: top, Mode: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sv.Selectors) != 1 || sv.Selectors[0] != "face(feature=plate,side=+Z)" || len(sv.Highlights) != 1 || sv.Entities[0].Area != 6000 {
		t.Fatalf("selection %+v", sv)
	}

	s.NewProvider = scripted(t, `add_hole {"face":"face(feature=plate,side=+Z)","diameter":3.2,"positions":[[10,10],[90,10],[10,50],[90,50]],"intent":"Four M3 clearance holes"}`)
	if err := s.Prompt("Add four M3 holes here", true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	st, _ = s.State()
	if len(st.Pending) != 0 || len(st.Draft) != 1 || len(st.Tree) != 2 {
		t.Fatalf("review turn should apply one draft step; pending=%+v draft=%v tree=%d", st.Pending, st.Draft, len(st.Tree))
	}
	if st.Chat[len(st.Chat)-1].Role != "draft" {
		t.Fatalf("chat %+v", st.Chat)
	}
	if err := s.KeepDraft(); err != nil {
		t.Fatal(err)
	}
	st, _ = s.State()
	if len(st.Draft) != 0 || len(st.Selection.Selectors) != 0 {
		t.Fatalf("after keep: draft=%v sel=%v", st.Draft, st.Selection.Selectors)
	}
	if last := st.Checkpoints[len(st.Checkpoints)-1]; last.Label != "Add four M3 holes here" || !last.Current {
		t.Fatalf("semantic checkpoint %+v", last)
	}
	vol := st.Volume
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.State(); len(st.Tree) != 1 || st.Volume <= vol || !st.CanRedo {
		t.Fatalf("undo: tree=%d vol=%v", len(st.Tree), st.Volume)
	}
	// Provenance: everything traces to validated tool calls.
	if n := len(s.app.AH.ToolCalls); n != 2 {
		t.Fatalf("expected 2 recorded tool calls, got %d", n)
	}
}

func TestEditsRejectedWhileAgentWorks(t *testing.T) {
	s := newSvc(t)
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	if _, err := s.CallTool("create_box", json.RawMessage(`{"dx":1,"dy":1,"dz":1}`)); err != ErrBusy {
		t.Fatalf("got %v", err)
	}
	if err := s.Prompt("hello", true); err != ErrBusy {
		t.Fatalf("got %v", err)
	}
	if _, err := s.State(); err != nil {
		t.Fatal("reads must stay available while the agent works")
	}
}

func TestParameterEditTimelineCompareAndPlayback(t *testing.T) {
	s := newSvc(t)
	if err := s.SetLevel(LevelAuto); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{`{"id":"plate","dx":50,"dy":30,"dz":4}`} {
		if o, err := s.CallTool("create_box", json.RawMessage(a)); err != nil || !o.Committed {
			t.Fatalf("%v %+v", err, o)
		}
	}
	if o, err := s.CallTool("add_hole", json.RawMessage(`{"target":"plate","diameter":4,"positions":[[10,10],[40,20]]}`)); err != nil || !o.Committed {
		t.Fatalf("%v %+v", err, o)
	}
	if o, err := s.SetParameter("plate", "dz", 6.0); err != nil || !o.OK || !o.Committed {
		t.Fatalf("inline edit: %v %+v", err, o)
	}
	st, _ := s.State()
	if st.Tree[0].Params["dz"] != 6.0 || len(st.Checkpoints) != 3 {
		t.Fatalf("tree %+v checkpoints %d", st.Tree[0].Params, len(st.Checkpoints))
	}
	cmp, err := s.Compare("cp_001", "")
	if err != nil || cmp.Identical || cmp.FeaturesB != 2 || cmp.VolumeB >= cmp.VolumeA*2 {
		t.Fatalf("compare %+v %v", cmp, err)
	}
	// Restore is proposed, never auto-committed, even at the auto level.
	o, err := s.Restore("cp_001")
	if err != nil || !o.OK || o.Committed || o.ChangeSet == nil {
		t.Fatalf("restore %+v %v", o, err)
	}
	if err := s.Reject(o.ChangeSet.ID); err != nil {
		t.Fatal(err)
	}

	// Playback rebuilds the design feature by feature from the committed ChangeSets.
	f0, err := s.PlaybackFrame(0, nil)
	if err != nil || f0.Total != 3 || len(f0.Scene.Changed) != 0 {
		t.Fatalf("frame 0 %+v %v", f0, err)
	}
	f1, _ := s.PlaybackFrame(1, nil)
	f3, _ := s.PlaybackFrame(3, f1.Scene.Keys)
	if len(f1.Scene.Changed) != 1 || f1.ChangeSet.Intent == "" || len(f3.Scene.Changed) != 1 || f3.Scene.Changed[0].Triangles <= f1.Scene.Changed[0].Triangles/2 {
		t.Fatalf("frames %+v %+v", f1.Scene.Stats, f3.Scene.Stats)
	}
	if _, err := s.PlaybackFrame(9, nil); err == nil {
		t.Fatal("out-of-range frame must fail")
	}
}

func TestSelectionModesAndStaleCleanup(t *testing.T) {
	s := newSvc(t)
	s.SetLevel(LevelAuto)
	s.CallTool("create_box", json.RawMessage(`{"id":"b","dx":20,"dy":20,"dz":20}`))
	if _, err := s.Pick(PickRequest{Body: "b", Kind: "face", Index: 0, Triangle: -1}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Pick(PickRequest{Body: "b", Kind: "edge", Index: 3, Triangle: -1, Mode: "add"})
	if len(v.Selectors) != 2 || len(v.Highlights) != 2 {
		t.Fatalf("add mode %+v", v)
	}
	v, _ = s.Pick(PickRequest{Body: "b", Kind: "edge", Index: 3, Triangle: -1, Mode: "toggle"})
	if len(v.Selectors) != 1 {
		t.Fatalf("toggle %+v", v)
	}
	v, _ = s.Pick(PickRequest{}) // click on empty space clears
	if len(v.Selectors) != 0 {
		t.Fatalf("clear %+v", v)
	}
	if _, err := s.Pick(PickRequest{Body: "ghost", Kind: "face", Triangle: -1}); err == nil {
		t.Fatal("unknown body must fail")
	}
	if _, err := s.Pick(PickRequest{Body: "b", Kind: "face", Index: 99, Triangle: -1}); err == nil {
		t.Fatal("out-of-range pick must fail")
	}
	// A selection that stops resolving is dropped when the model changes under it.
	s.Pick(PickRequest{Body: "b", Kind: "face", Index: 0, Triangle: -1})
	o, _ := s.CallTool("remove_feature", json.RawMessage(`{"feature":"b"}`))
	if _, err := s.Commit(o.ChangeSet.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.State(); len(st.Selection.Selectors) != 0 {
		t.Fatalf("stale selection survived: %+v", st.Selection)
	}
}

func TestPaletteListsEveryToolAndIntentVerb(t *testing.T) {
	s := newSvc(t)
	kinds := map[string]int{}
	tools := map[string]bool{}
	for _, e := range s.Palette() {
		kinds[e.Kind]++
		if e.Kind == "tool" {
			tools[e.ID] = true
		}
	}
	if kinds["intent"] != len(IntentVerbs) || kinds["command"] < 5 {
		t.Fatalf("palette %v", kinds)
	}
	for _, n := range s.app.Reg.Names() {
		if !tools[n] {
			t.Fatalf("tool %s missing from palette", n)
		}
	}
}

func TestReviewTurnRunsSeveralStepsAndDiscards(t *testing.T) {
	s := newSvc(t)
	s.NewProvider = scripted(t,
		`create_box {"id":"cube","dx":50,"dy":50,"dz":50}`,
		`fillet {"target":"cube","radius":4}`)
	if err := s.Prompt("rounded cube", true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	st, _ := s.State()
	if len(st.Draft) != 2 || len(st.Tree) != 2 || len(st.Pending) != 0 {
		t.Fatalf("draft=%v tree=%d pending=%d", st.Draft, len(st.Tree), len(st.Pending))
	}
	if err := s.DiscardDraft(); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.State(); len(st.Tree) != 0 || len(st.Draft) != 0 {
		t.Fatalf("after discard: tree=%d draft=%v", len(st.Tree), st.Draft)
	}
}

// Toggling visibility at the Review level applies immediately: nothing pending, no draft,
// no new checkpoint.
func TestAppearanceNeedsNoApprovalInReview(t *testing.T) {
	s := newSvc(t)
	s.CallTool("create_box", json.RawMessage(`{"id":"plate","dx":100,"dy":60,"dz":4}`))
	st, _ := s.State()
	if _, err := s.Commit(st.Pending[0].ID); err != nil {
		t.Fatal(err)
	}
	before := len(s.app.Hist.Checkpoints)
	o, err := s.CallTool("set_visibility", json.RawMessage(`{"features":["plate"],"visible":false}`))
	if err != nil || !o.OK || !o.Committed {
		t.Fatalf("visibility should commit at Review level: %+v %v", o, err)
	}
	st, _ = s.State()
	if len(st.Pending) != 0 || len(st.Draft) != 0 || len(s.app.Hist.Checkpoints) != before {
		t.Fatalf("pending=%d draft=%d checkpoints %d->%d", len(st.Pending), len(st.Draft), before, len(s.app.Hist.Checkpoints))
	}
}

func TestDeleteSelectionThroughService(t *testing.T) {
	s := newSvc(t)
	for i, call := range []struct{ tool, args string }{
		{"create_box", `{"id":"b","dx":20,"dy":20,"dz":5}`},
		{"create_box", `{"id":"c","dx":5,"dy":5,"dz":5}`},
		{"add_hole", `{"target":"b","diameter":3}`},
	} {
		o, _ := s.CallTool(call.tool, json.RawMessage(call.args))
		if !o.OK {
			t.Fatalf("step %d: %s", i, o.Error)
		}
		if !o.Committed {
			if _, err := s.Commit(o.ChangeSet.ID); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Faces/edges are never deleted: the service asks whether to delete the owning feature.
	s.Pick(PickRequest{Body: "hole", Kind: "face", Index: 0, Triangle: -1})
	r, err := s.Delete(DeleteRequest{})
	if err != nil || r.NeedsConfirm != "owner" || r.Analysis.SubEntities[0].Owner != "hole" {
		t.Fatalf("want owner question, got %+v err=%v", r, err)
	}
	if st, _ := s.State(); len(st.Tree) != 3 {
		t.Fatal("a question must not change the model")
	}

	// A feature other features depend on warns first.
	r, err = s.Delete(DeleteRequest{Features: []string{"b"}})
	if err != nil || r.NeedsConfirm != "dependents" || len(r.Analysis.Dependents) != 1 {
		t.Fatalf("want dependents question, got %+v err=%v", r, err)
	}
	// Confirming deletes target + dependents in ONE ChangeSet, committed and undoable.
	r, err = s.Delete(DeleteRequest{Features: []string{"b"}, Cascade: true})
	if err != nil || !r.OK || !r.Committed || len(r.ChangeSet.Removed) != 2 {
		t.Fatalf("cascade delete: %+v err=%v", r, err)
	}
	st, _ := s.State()
	if len(st.Tree) != 1 || st.Tree[0].ID != "c" || !st.CanUndo {
		t.Fatalf("tree after delete: %+v", st.Tree)
	}
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.State(); len(st.Tree) != 3 {
		t.Fatalf("undo: %d nodes", len(st.Tree))
	}
	if _, err := s.Redo(); err != nil {
		t.Fatal(err)
	}

	// A selected body deletes straight away and the selection is cleared.
	s.Pick(PickRequest{Body: "c", Kind: "body", Index: -1, Triangle: -1})
	r, err = s.Delete(DeleteRequest{})
	if err != nil || !r.OK || !r.Committed {
		t.Fatalf("selection delete: %+v err=%v", r, err)
	}
	if st, _ = s.State(); len(st.Tree) != 0 || len(st.Selection.Selectors) != 0 {
		t.Fatalf("after delete: tree=%d sel=%v", len(st.Tree), st.Selection.Selectors)
	}
}

// "delete these objects" from the agent: delete_objects over the selection, one ChangeSet,
// and discarding the draft undoes it.
func TestAgentDeletesSelectedObjects(t *testing.T) {
	s := newSvc(t)
	s.SetLevel(LevelAuto)
	s.CallTool("create_box", json.RawMessage(`{"id":"a","dx":10,"dy":10,"dz":5}`))
	s.CallTool("create_box", json.RawMessage(`{"id":"b","dx":10,"dy":10,"dz":5}`))
	s.Pick(PickRequest{Body: "a", Kind: "body", Index: -1, Triangle: -1})
	s.Pick(PickRequest{Body: "b", Kind: "body", Index: -1, Triangle: -1, Mode: "add"})

	// At the Auto level a delete is never low-risk: it waits for approval.
	s.NewProvider = scripted(t, `delete_objects {"intent":"Remove the selected parts"}`)
	if err := s.Prompt("delete these objects", false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if st, _ := s.State(); len(st.Tree) != 2 || len(st.Pending) != 1 {
		t.Fatalf("auto level must leave the delete pending: tree=%d pending=%d", len(st.Tree), len(st.Pending))
	}
	if err := s.Reject(s.app.Pending()[0].ID); err != nil {
		t.Fatal(err)
	}

	s.SetLevel(LevelReview)
	s.NewProvider = scripted(t, `delete_objects {"intent":"Remove the selected parts"}`)
	if err := s.Prompt("delete these objects", false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	st, _ := s.State()
	if len(st.Tree) != 0 || len(st.Draft) != 1 {
		t.Fatalf("agent delete should apply one draft ChangeSet: tree=%d draft=%v pending=%d", len(st.Tree), st.Draft, len(st.Pending))
	}
	if err := s.DiscardDraft(); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.State(); len(st.Tree) != 2 {
		t.Fatalf("discard must restore both objects, tree=%d", len(st.Tree))
	}
}

// A typed dimension is applied at once even in Review (like an inline parameter edit), stays
// undoable, and the object stays selected with its updated dimensions for the next edit.
func TestDimensionEditInReviewKeepsTheObjectSelected(t *testing.T) {
	s := newSvc(t)
	s.CallTool("create_box", json.RawMessage(`{"id":"plate","dx":120,"dy":80,"dz":40}`))
	st, _ := s.State()
	if _, err := s.Commit(st.Pending[0].ID); err != nil {
		t.Fatal(err)
	}
	sel, err := s.Pick(PickRequest{Body: "plate", Kind: "face", Index: 0, Triangle: -1})
	if err != nil || len(sel.Dimensions) != 1 || sel.Dimensions[0].Width != 120 {
		t.Fatalf("a selected face shows its object's dimensions: %+v %v", sel.Dimensions, err)
	}
	o, err := s.CallTool("set_object_dimension", json.RawMessage(`{"axis":"width","value":150}`))
	if err != nil || !o.OK || !o.Committed {
		t.Fatalf("a dimension edit should commit at Review level: %+v %v", o, err)
	}
	v := s.Selection()
	if len(v.Selectors) != 1 || v.Selectors[0] != "body(feature=resize)" || len(v.Dimensions) != 1 {
		t.Fatalf("the resized object stays selected: %+v", v)
	}
	if d := v.Dimensions[0]; d.Width != 150 || d.Depth != 80 || d.Height != 40 {
		t.Fatalf("dimensions after the edit: %+v", d)
	}
	// The next edit folds into the same resize feature: the object is still selected.
	if o, _ := s.CallTool("set_object_dimension", json.RawMessage(`{"object_id":"resize","axis":"height","value":25}`)); !o.Committed {
		t.Fatalf("second edit: %+v", o)
	}
	if v = s.Selection(); len(v.Dimensions) != 1 || v.Dimensions[0].Height != 25 || v.Dimensions[0].Width != 150 {
		t.Fatalf("after a folded edit: %+v", v)
	}
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.State(); st.CanUndo != true || len(st.Pending) != 0 {
		t.Fatalf("undo: %+v", st)
	}
}

// The viewport's interactive Move/Rotate commit through CallTool: applied at once even at Review
// (direct manipulation, like a typed dimension), one undo step, and the moved object stays
// selected with its transform available for the next drag.
func TestInteractiveTransformCommitsAtReviewAndKeepsSelection(t *testing.T) {
	s := newSvc(t)
	s.CallTool("create_box", json.RawMessage(`{"id":"plate","dx":120,"dy":80,"dz":40}`))
	st, _ := s.State()
	if _, err := s.Commit(st.Pending[0].ID); err != nil {
		t.Fatal(err)
	}
	sel, err := s.Pick(PickRequest{Body: "plate", Kind: "face", Index: 0, Triangle: -1})
	if err != nil || len(sel.Transforms) != 1 || !sel.Transforms[0].Transformable || sel.Transforms[0].Origin != [3]float64{} {
		t.Fatalf("a selected object carries its transform: %+v %v", sel.Transforms, err)
	}
	o, err := s.CallTool("move_object", json.RawMessage(`{"object_id":"plate","dx":12.5,"dy":0,"dz":0}`))
	if err != nil || !o.OK || !o.Committed {
		t.Fatalf("an interactive move commits at Review level: %+v %v", o, err)
	}
	v := s.Selection()
	if len(v.Selectors) != 1 || v.Selectors[0] != "body(feature=move)" || len(v.Transforms) != 1 || v.Transforms[0].Origin != [3]float64{12.5, 0, 0} {
		t.Fatalf("the moved object stays selected: %+v", v)
	}
	if o, _ := s.CallTool("rotate_object", json.RawMessage(`{"angle":90,"axis":"z","pivot":"center"}`)); !o.Committed {
		t.Fatalf("rotating the selected object: %+v", o)
	}
	if v = s.Selection(); len(v.Selectors) != 1 || v.Selectors[0] != "body(feature=rotate)" {
		t.Fatalf("the rotated object stays selected: %+v", v.Selectors)
	}
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.State(); !st.CanUndo || !st.CanRedo {
		t.Fatal("one undo step per transform")
	}
}

func histMsg(role string, n int) runtime.Message {
	return runtime.Message{Role: role, Content: []runtime.Block{{Type: "text", Text: strings.Repeat("x", n)}}}
}

func TestTrimHistoryDropsOldestToFitTheSizeBudget(t *testing.T) {
	in := []runtime.Message{histMsg("user", 400), histMsg("assistant", 400), histMsg("user", 400), histMsg("assistant", 400)}
	// 1600 chars do not fit 1000: the two oldest go, leaving 800 that start with a user turn
	out := trimHistory(in, 30, 1000)
	if len(out) != 2 || out[0].Role != "user" || out[1].Role != "assistant" {
		t.Fatalf("want the newest user/assistant pair, got %d messages starting with %q", len(out), out[0].Role)
	}
	// when dropping leaves an assistant message first, it is dropped too
	out = trimHistory(in, 30, 1200)
	if len(out) != 2 || out[0].Role != "user" {
		t.Fatalf("history must start with a user message: %+v", out)
	}
}

func TestTrimHistoryKeepsAnythingThatFits(t *testing.T) {
	in := []runtime.Message{histMsg("user", 10), histMsg("assistant", 10), histMsg("user", 10)}
	if out := trimHistory(in, 30, 1000); len(out) != 3 {
		t.Fatalf("nothing should be dropped: %d", len(out))
	}
	if out := trimHistory(in, 2, 1000); len(out) != 1 || out[0].Role != "user" {
		t.Fatalf("message cap then leading-user rule: %+v", out)
	}
}

func TestTrimHistoryTruncatesAnOversizedLastMessage(t *testing.T) {
	out := trimHistory([]runtime.Message{histMsg("user", 5000)}, 30, 1000)
	if len(out) != 1 || len(out[0].Content[0].Text) > 1010 || !strings.HasPrefix(out[0].Content[0].Text, "…") {
		t.Fatalf("a single huge message must be cut to the budget, keeping its end")
	}
}

// The selection notice carries the new selection, so the page can apply it without
// refetching the whole state (chat and tree included).
func TestSelectionNoticeCarriesTheSelection(t *testing.T) {
	s := newSvc(t)
	s.SetLevel(LevelAuto)
	s.CallTool("create_box", json.RawMessage(`{"id":"b","dx":20,"dy":20,"dz":20}`))
	ch, cancel := s.Hub().Subscribe()
	defer cancel()
	want, err := s.Pick(PickRequest{Body: "b", Kind: "face", Index: 0, Triangle: -1})
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case n := <-ch:
			if n.Kind != "selection" {
				continue
			}
			if n.Selection == nil || len(n.Selection.Selectors) != 1 || n.Selection.Selectors[0] != want.Selectors[0] {
				t.Fatalf("selection notice = %+v, want selectors %v", n.Selection, want.Selectors)
			}
			return
		case <-time.After(3 * time.Second):
			t.Fatal("no selection notice")
		}
	}
}

// While an agent turn runs (the lock is released during model calls) nothing else may change
// the state it works on.
func TestStateChangingCallsAreRefusedWhileAgentIsBusy(t *testing.T) {
	s := newSvc(t)
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	checks := map[string]error{}
	_, checks["save"] = s.Save(t.TempDir())
	_, checks["export"] = s.Export("stl", filepath.Join(t.TempDir(), "x.stl"))
	_, checks["selection"] = s.SetSelection(nil)
	_, checks["spec"] = s.AddSpec("a.txt", []byte("hello"))
	_, checks["image"] = s.UploadImage([]byte("x"))
	checks["level"] = s.SetLevel(LevelAuto)
	for name, err := range checks {
		if err != ErrBusy {
			t.Errorf("%s while busy: %v, want ErrBusy", name, err)
		}
	}
}
