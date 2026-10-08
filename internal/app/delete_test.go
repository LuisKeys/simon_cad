package app

import (
	"github.com/accelone/simoncad/internal/tools"
	"strings"
	"testing"
)

func newPlate(t *testing.T) *App {
	t.Helper()
	a := New("demo", clock)
	t.Cleanup(a.Close)
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":40,"dy":40,"dz":4}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"lid","dx":10,"dy":10,"dz":4}`), PolicyAuto))
	return a
}

func TestDeleteSingleSelectedBodyIsOneUndoableChangeSet(t *testing.T) {
	a := newPlate(t)
	if err := a.SetSelection([]string{"body(feature=lid)"}); err != nil {
		t.Fatal(err)
	}
	n := len(a.Mgr.Log)
	// Destructive: never auto-commits, even under PolicyAuto.
	o := mustOK(t, a.ApplyTool("delete_objects", []byte(`{}`), PolicyAuto))
	if o.Committed || a.Graph.Get("lid") == nil || len(a.Pending()) != 1 {
		t.Fatalf("delete must wait for approval under auto policy: %+v", o)
	}
	if err := a.Reject(o.ChangeSet.ID); err != nil {
		t.Fatal(err)
	}
	o = mustOK(t, a.ApplyTool("delete_objects", []byte(`{}`), PolicyCommit))
	if !o.Committed || a.Graph.Get("lid") != nil || a.Graph.Get("plate") == nil {
		t.Fatalf("delete failed: %+v", o)
	}
	if len(a.Mgr.Log) != n+2 || len(o.ChangeSet.Commands) != 1 || o.ChangeSet.Commands[0].Type != "RemoveFeature" {
		t.Fatalf("want a RemoveFeature ChangeSet, got %+v", o.ChangeSet.Commands)
	}
	if _, err := a.Undo(); err != nil || a.Graph.Get("lid") == nil {
		t.Fatalf("undo: %v", err)
	}
	if _, err := a.Redo(); err != nil || a.Graph.Get("lid") != nil {
		t.Fatalf("redo: %v", err)
	}
}

func TestDeleteMultipleIsOneChangeSet(t *testing.T) {
	a := newPlate(t)
	before := a.Graph.Hash()
	if err := a.SetSelection([]string{"body(feature=plate)", "body(feature=lid)"}); err != nil {
		t.Fatal(err)
	}
	n := len(a.Mgr.Log)
	o := mustOK(t, a.ApplyTool("delete_objects", []byte(`{}`), PolicyCommit))
	if len(a.Mgr.Log) != n+1 || len(o.ChangeSet.Commands) != 2 || len(a.Graph.Nodes) != 0 {
		t.Fatalf("want one 2-command ChangeSet: %+v nodes=%d", o.ChangeSet.Commands, len(a.Graph.Nodes))
	}
	if _, err := a.Undo(); err != nil || a.Graph.Hash() != before {
		t.Fatalf("one undo must restore both objects exactly: %v", err)
	}
	if _, err := a.Redo(); err != nil || len(a.Graph.Nodes) != 0 {
		t.Fatalf("redo: %v", err)
	}
}

func TestDeleteWarnsAboutDependents(t *testing.T) {
	a := newPlate(t)
	mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"plate","diameter":5}`), PolicyAuto))
	before := a.Graph.Hash()

	// Dry run reports the dependents without changing anything.
	o := mustOK(t, a.ApplyTool("check_delete", []byte(`{"features":["plate"]}`), PolicyPreview))
	if o.ChangeSet != nil || a.Graph.Hash() != before {
		t.Fatal("check_delete must not propose or change anything")
	}
	if s := strings.Join(o.Data.(*tools.DeleteAnalysis).Dependents, ","); s != "hole" {
		t.Fatalf("dependents = %q", s)
	}

	// Without cascade the delete is refused and names the dependents.
	o = a.ApplyTool("delete_objects", []byte(`{"features":["plate"]}`), PolicyCommit)
	if o.OK || !strings.Contains(o.Error, "hole") || a.Graph.Hash() != before {
		t.Fatalf("want refusal naming hole, got %+v", o)
	}

	// With cascade, dependents go first, in the same ChangeSet, with a warning.
	o = mustOK(t, a.ApplyTool("delete_objects", []byte(`{"features":["plate"],"cascade":true}`), PolicyPreview))
	if o.Committed || len(o.ChangeSet.Commands) != 2 || len(o.ChangeSet.Warnings) == 0 || !strings.Contains(strings.Join(o.ChangeSet.Warnings, ";"), "hole") {
		t.Fatalf("cascade ChangeSet must carry a dependency warning: %+v", o.ChangeSet)
	}
	if _, err := a.Commit(o.ChangeSet.ID); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Get("plate") != nil || a.Graph.Get("hole") != nil || a.Graph.Get("lid") == nil {
		t.Fatal("cascade removed the wrong features")
	}
	if _, err := a.Undo(); err != nil || a.Graph.Hash() != before {
		t.Fatalf("undo of cascade must restore the exact graph: %v", err)
	}
}

func TestDeleteFaceOrEdgeNeverTouchesTopology(t *testing.T) {
	a := newPlate(t)
	before := a.Graph.Hash()
	for _, sel := range []string{"face(feature=plate,side=+Z)", "edge(feature=plate,index=3)"} {
		if err := a.SetSelection([]string{sel}); err != nil {
			t.Fatalf("%s: %v", sel, err)
		}
		o := a.ApplyTool("delete_objects", []byte(`{}`), PolicyCommit)
		if o.OK || o.ChangeSet != nil || !strings.Contains(o.Error, "feature plate") || !strings.Contains(o.Error, "cannot be deleted directly") {
			t.Fatalf("%s: want rejection pointing at the owning feature, got %+v", sel, o)
		}
		if a.Graph.Hash() != before {
			t.Fatalf("%s: model changed", sel)
		}
		// The analysis resolves it to the owner and offers it.
		c := mustOK(t, a.ApplyTool("check_delete", []byte(`{}`), PolicyPreview))
		an := c.Data.(*tools.DeleteAnalysis)
		if len(an.SubEntities) != 1 || an.SubEntities[0].Owner != "plate" || len(an.Targets) != 0 {
			t.Fatalf("%s: analysis %+v", sel, an)
		}
		// Accepting the offer deletes the owning feature, as a normal feature ChangeSet.
		o = mustOK(t, a.ApplyTool("delete_objects", []byte(`{"resolve_owner":true}`), PolicyCommit))
		if a.Graph.Get("plate") != nil || o.ChangeSet.Commands[0].Type != "RemoveFeature" {
			t.Fatalf("%s: owner not removed: %+v", sel, o)
		}
		if _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeleteRejectsEmptyAndUnknown(t *testing.T) {
	a := newPlate(t)
	if o := a.ApplyTool("delete_objects", []byte(`{}`), PolicyCommit); o.OK || !strings.Contains(o.Error, "nothing to delete") {
		t.Fatalf("empty selection: %+v", o)
	}
	if o := a.ApplyTool("delete_objects", []byte(`{"features":["ghost"]}`), PolicyCommit); o.OK {
		t.Fatalf("unknown feature: %+v", o)
	}
}
