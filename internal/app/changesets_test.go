package app

import (
	"strings"
	"testing"
)

func commitTool(t *testing.T, a *App, tool, args string) string {
	t.Helper()
	o := a.ApplyTool(tool, []byte(args), PolicyCommit)
	if !o.OK || !o.Committed {
		t.Fatalf("%s: %+v", tool, o)
	}
	return o.ChangeSet.ID
}

func TestRevertLatestUndoesNewestFirst(t *testing.T) {
	a := New("t", clock)
	defer a.Close()
	first := commitTool(t, a, "create_box", `{"id":"a","dx":10,"dy":10,"dz":10}`)
	second := commitTool(t, a, "create_box", `{"id":"b","dx":5,"dy":5,"dz":5}`)
	third := commitTool(t, a, "create_box", `{"id":"c","dx":2,"dy":2,"dz":2}`)

	done, err := a.RevertLatest([]string{second, third})
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 || done[0] != third || done[1] != second {
		t.Fatalf("reverted %v, want [%s %s] (newest first)", done, third, second)
	}
	if g := a.Graph; g.Get("c") != nil || g.Get("b") != nil || g.Get("a") == nil {
		t.Fatalf("only the first feature should remain: %d nodes", len(g.Nodes))
	}
	if _, err := a.RevertLatest([]string{first}); err != nil {
		t.Fatal(err)
	}
	if len(a.Graph.Nodes) != 0 {
		t.Fatal("everything should be reverted")
	}
}

// If something else was committed after the ChangeSets being reverted, the revert stops
// without undoing it: the model must not lose work it was not asked to drop.
func TestRevertLatestStopsWhenTheModelChangedMeanwhile(t *testing.T) {
	a := New("t", clock)
	defer a.Close()
	mine := commitTool(t, a, "create_box", `{"id":"a","dx":10,"dy":10,"dz":10}`)
	commitTool(t, a, "create_box", `{"id":"later","dx":5,"dy":5,"dz":5}`)

	done, err := a.RevertLatest([]string{mine})
	if err == nil || !strings.Contains(err.Error(), "could not revert "+mine) {
		t.Fatalf("want a 'could not revert' error, got %v", err)
	}
	if len(done) != 0 {
		t.Fatalf("nothing should count as reverted: %v", done)
	}
	if a.Graph.Get("later") == nil || a.Graph.Get("a") == nil {
		t.Fatal("the later ChangeSet must be restored after the aborted undo")
	}
}

func TestRevertLatestWithNothingToDo(t *testing.T) {
	a := New("t", clock)
	defer a.Close()
	if done, err := a.RevertLatest(nil); err != nil || len(done) != 0 {
		t.Fatalf("%v %v", done, err)
	}
	if _, err := a.RevertLatest([]string{"cs_9999"}); err == nil {
		t.Fatal("undo with an empty stack must fail")
	}
}
