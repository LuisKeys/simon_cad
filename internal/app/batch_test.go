package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/tools"
)

func batchOf(calls ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"calls": calls})
	return b
}

func batchCall(tool string, args map[string]any) map[string]any {
	return map[string]any{"tool": tool, "args": args}
}

func threeBoxes(t *testing.T) *App {
	t.Helper()
	a := New("t", clock)
	for _, id := range []string{"a", "b", "c"} {
		if o := a.ApplyTool("create_box", []byte(fmt.Sprintf(`{"id":%q,"dx":10,"dy":10,"dz":10}`, id)), PolicyCommit); !o.OK {
			t.Fatal(o.Error)
		}
	}
	return a
}

// Several edits as one ChangeSet: one undo brings every object back, and the generated ids
// of the scale features do not collide.
func TestBatchIsOneChangeSetAndOneUndo(t *testing.T) {
	a := threeBoxes(t)
	defer a.Close()
	before := len(a.Mgr.CommittedIDs())
	o := a.ApplyTool("batch", batchOf(
		batchCall("scale", map[string]any{"target": "a", "factor": 2}),
		batchCall("scale", map[string]any{"target": "b", "factor": 2}),
		batchCall("scale", map[string]any{"target": "c", "factor": 2}),
	), PolicyCommit)
	if !o.OK || !o.Committed {
		t.Fatalf("%+v", o)
	}
	if n := len(a.Mgr.CommittedIDs()) - before; n != 1 {
		t.Fatalf("a batch is one ChangeSet, got %d", n)
	}
	scales := 0
	for _, n := range a.Graph.Nodes {
		if n.Type == "scale" {
			scales++
		}
	}
	if scales != 3 {
		t.Fatalf("want 3 scale features, got %d", scales)
	}
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	for _, n := range a.Graph.Nodes {
		if n.Type == "scale" {
			t.Fatalf("one undo must remove every scale, %q is left", n.ID)
		}
	}
	if len(a.Graph.Nodes) != 3 {
		t.Fatalf("the boxes must remain: %d nodes", len(a.Graph.Nodes))
	}
}

// If any call fails, nothing is applied and the message says which call it was.
func TestBatchIsAllOrNothing(t *testing.T) {
	a := threeBoxes(t)
	defer a.Close()
	nodes, committed := len(a.Graph.Nodes), len(a.Mgr.CommittedIDs())
	o := a.ApplyTool("batch", batchOf(
		batchCall("scale", map[string]any{"target": "a", "factor": 2}),
		batchCall("scale", map[string]any{"target": "missing", "factor": 2}),
		batchCall("scale", map[string]any{"target": "c", "factor": 2}),
	), PolicyCommit)
	if o.OK || !strings.Contains(o.Error, "call 2 (scale)") {
		t.Fatalf("want a failure naming call 2, got %+v", o)
	}
	if len(a.Graph.Nodes) != nodes || len(a.Mgr.CommittedIDs()) != committed {
		t.Fatal("a failed batch must change nothing")
	}
}

func TestBatchRefusesWhatItShouldNot(t *testing.T) {
	a := threeBoxes(t)
	defer a.Close()
	for name, calls := range map[string][]map[string]any{
		"unknown tool":     {batchCall("no_such_tool", nil)},
		"a query":          {batchCall("get_model_summary", nil)},
		"a nested batch":   {batchCall("batch", map[string]any{"calls": []any{batchCall("scale", map[string]any{"target": "a", "factor": 2})}})},
		"invalid args":     {batchCall("scale", map[string]any{"factor": "big"})},
		"an unsafe export": {batchCall("export_step", map[string]any{"path": "x.step"})},
	} {
		if o := a.ApplyTool("batch", batchOf(calls...), PolicyCommit); o.OK {
			t.Errorf("%s was accepted", name)
		}
	}
	if o := a.ApplyTool("batch", []byte(`{"calls":[]}`), PolicyCommit); o.OK {
		t.Error("an empty batch was accepted")
	}
}

// Under the auto policy a batch commits by itself only when each of its calls would have.
func TestBatchFollowsTheAutoPolicy(t *testing.T) {
	a := threeBoxes(t)
	defer a.Close()
	o := a.ApplyTool("batch", batchOf(
		batchCall("suppress_feature", map[string]any{"feature": "a", "suppressed": true}),
		batchCall("suppress_feature", map[string]any{"feature": "b", "suppressed": true}),
	), PolicyAuto)
	if !o.OK || !o.Committed {
		t.Fatalf("a batch of low-risk edits commits under auto: %+v", o)
	}
	o = a.ApplyTool("batch", batchOf(
		batchCall("suppress_feature", map[string]any{"feature": "c", "suppressed": true}),
		batchCall("remove_feature", map[string]any{"feature": "a"}),
	), PolicyAuto)
	if !o.OK || o.Committed {
		t.Fatalf("a batch with a destructive call must wait for approval: %+v", o)
	}
	if len(a.Pending()) != 1 {
		t.Fatalf("pending = %d", len(a.Pending()))
	}
}

// The agent must not be able to use a batch to get around the per-iteration limit on
// structural changes: its capabilities do not include the tool.
func TestBatchIsNotOfferedToTheAgent(t *testing.T) {
	a := New("t", clock)
	defer a.Close()
	if _, ok := a.Reg.Get("batch"); !ok {
		t.Fatal("batch must be registered for the UI")
	}
	for _, c := range []string{tools.CapGeometry, tools.CapVision, tools.CapVisual} {
		for _, tl := range a.Reg.Subset(c) {
			if tl.Name == "batch" {
				t.Fatalf("batch is in the %s capability the agent receives", c)
			}
		}
	}
}
