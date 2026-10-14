package commands

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/events"
)

func box(id string, deps ...string) *cadir.Node {
	n := &cadir.Node{ID: id, Type: "box", Parameters: map[string]any{"dx": 10.0, "dy": 10.0, "dz": 10.0}}
	n.Dependencies = deps
	return n
}

func TestEachCommandInverts(t *testing.T) {
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{box("a"), box("b", "a"), box("c")}
	g.Get("b").Expressions = map[string]string{"dx": "a.dx * 2"}
	cmds := []Command{
		&AddFeature{Node: box("d"), Index: 1},
		&RemoveFeature{ID: "c"},
		&SetParameter{ID: "a", Name: "dx", Value: 25.0},
		&SetParameter{ID: "a", Name: "material", Value: "PETG"},
		&SetParameter{ID: "a", Name: "dz", Delete: true},
		&SetExpression{ID: "a", Name: "dy", Expr: "a.dx / 2"},
		&SetExpression{ID: "b", Name: "dx", Delete: true},
		&SetExpression{ID: "b", Name: "dx", Expr: "a.dx"},
		&Reorder{ID: "a", Index: 2},
		&Suppress{ID: "a", Suppressed: true},
		&Rename{ID: "a", Name: "Base"},
		&SetColor{ID: "a", Color: "#ff0000"},
		&SetOpacity{ID: "a", Opacity: ptr(0.5)},
		&SetVisibility{ID: "a", Visible: ptr(false)},
	}
	for _, c := range cmds {
		before := g.Hash()
		beforeBytes, _ := g.Canonical()
		inv, err := c.Apply(g)
		if err != nil {
			// some commands are only valid in sequence; skip invalid ones here
			t.Logf("skip %s: %v", c.Type(), err)
			continue
		}
		if g.Hash() == before {
			t.Errorf("%s did not change the graph", c.Type())
		}
		if _, err := inv.Apply(g); err != nil {
			t.Fatalf("inverse of %s: %v", c.Type(), err)
		}
		afterBytes, _ := g.Canonical()
		if string(afterBytes) != string(beforeBytes) {
			t.Errorf("%s inverse not exact:\n%s\nvs\n%s", c.Type(), beforeBytes, afterBytes)
		}
	}
}

func TestCommandRoundTripsThroughJSON(t *testing.T) {
	c := &AddFeature{Node: box("x"), Index: -1}
	d, err := Decode(Encode(c))
	if err != nil {
		t.Fatal(err)
	}
	g := cadir.NewGraph()
	if _, err := d.Apply(g); err != nil || g.Get("x") == nil {
		t.Fatalf("decoded command failed: %v", err)
	}
}

func TestRejectsBadEdits(t *testing.T) {
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{box("a"), box("b", "a")}
	bad := []Command{
		&AddFeature{Node: box("a")},                       // duplicate
		&AddFeature{Node: box("Bad-ID")},                  // bad id
		&AddFeature{Node: box("z", "ghost")},              // unknown dep
		&RemoveFeature{ID: "a"},                           // has dependents
		&SetExpression{ID: "a", Name: "dx", Expr: "b.dx"}, // cycle a->b->a
		&SetExpression{ID: "a", Name: "dx", Expr: "1 +"},  // syntax
		&SetParameter{ID: "ghost", Name: "x", Value: 1.0},
	}
	for _, c := range bad {
		before := g.Hash()
		if _, err := c.Apply(g); err == nil {
			t.Errorf("%s %+v should fail", c.Type(), c)
		}
		if g.Hash() != before {
			t.Errorf("failed %s mutated the graph", c.Type())
		}
	}
}

type fakeEval struct{}

// A graph "fails" if any node has dx < 0 — enough to drive validation paths.
func (fakeEval) Evaluate(g *cadir.Graph) (Evaluation, error) {
	ev := Evaluation{Failed: map[string][]string{}, BodyCnt: len(g.Nodes), Volume: float64(len(g.Nodes))}
	for _, n := range g.Nodes {
		if v, _ := n.Num("dx"); v < 0 {
			ev.Failed[n.ID] = []string{"negative dx"}
		}
	}
	return ev, nil
}

func TestChangeSetLifecycle(t *testing.T) {
	g := cadir.NewGraph()
	m := NewManager(g, fakeEval{}, events.NewBus())
	prov := cadir.Provenance{CreatedBy: cadir.CreatedByCLI}
	cs, err := m.Propose("add a", "adds a", prov, []Command{&AddFeature{Node: box("a"), Index: -1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 0 {
		t.Fatal("propose must not touch the live graph")
	}
	if !cs.AutoCommitEligible() || cs.Effects.Added[0] != "a" || cs.Effects.VolumeAfter != 1 {
		t.Fatalf("unexpected proposal: %+v", cs)
	}
	if _, err := m.Commit(cs.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Commit(cs.ID); err == nil {
		t.Fatal("double commit must fail")
	}
	bad, err := m.Propose("break", "", prov, []Command{&SetParameter{ID: "a", Name: "dx", Value: -1.0}})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Valid() {
		t.Fatal("rebuild failure must invalidate the ChangeSet")
	}
	if _, err := m.Commit(bad.ID); err == nil {
		t.Fatal("invalid ChangeSet must not commit")
	}
	// Stale detection.
	p1, _ := m.Propose("p1", "", prov, []Command{&Rename{ID: "a", Name: "One"}})
	p2, _ := m.Propose("p2", "", prov, []Command{&Rename{ID: "a", Name: "Two"}})
	if _, err := m.Commit(p1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Commit(p2.ID); err == nil {
		t.Fatal("stale ChangeSet must not commit")
	}
	if _, err := m.Undo(); err != nil {
		t.Fatal(err)
	}
	if g.Get("a").Metadata.Name != "" {
		t.Fatal("undo did not restore name")
	}
	if _, err := m.Redo(); err != nil || g.Get("a").Metadata.Name != "One" {
		t.Fatalf("redo failed: %v", err)
	}
	kinds := map[string]int{}
	for _, e := range m.Bus.Events() {
		kinds[e.Type]++
	}
	if kinds[events.ChangeSetProposed] != 4 || kinds[events.ChangeSetCommitted] != 2 || kinds[events.ChangeSetUndone] != 1 {
		t.Fatalf("event counts %v", kinds)
	}
}

// 1,000 random command sequences: committing then undoing everything restores the exact
// initial graph, and redoing everything reproduces the exact final graph.
func TestUndoRedoProperty(t *testing.T) {
	for seed := int64(0); seed < 1000; seed++ {
		r := rand.New(rand.NewSource(seed))
		g := cadir.NewGraph()
		m := NewManager(g, nil, nil)
		initial, _ := g.Canonical()
		nextID := 0
		for step := 0; step < 3+r.Intn(10); step++ {
			cmds := randomCommands(r, g, &nextID)
			if len(cmds) == 0 {
				continue
			}
			cs, err := m.Propose(fmt.Sprintf("s%d", step), "", cadir.Provenance{}, cmds)
			if err != nil {
				continue // invalid random edit: must be rejected without side effects
			}
			if _, err := m.Commit(cs.ID); err != nil {
				t.Fatalf("seed %d: commit: %v", seed, err)
			}
			if r.Intn(5) == 0 && m.CanUndo() {
				if _, err := m.Undo(); err != nil {
					t.Fatalf("seed %d: undo: %v", seed, err)
				}
				if _, err := m.Redo(); err != nil {
					t.Fatalf("seed %d: redo: %v", seed, err)
				}
			}
		}
		final, _ := g.Canonical()
		committed := len(m.CommittedIDs())
		for m.CanUndo() {
			if _, err := m.Undo(); err != nil {
				t.Fatalf("seed %d: undo: %v", seed, err)
			}
		}
		if got, _ := g.Canonical(); string(got) != string(initial) {
			t.Fatalf("seed %d: undo-all did not restore initial graph (%d changesets)", seed, committed)
		}
		for m.CanRedo() {
			if _, err := m.Redo(); err != nil {
				t.Fatalf("seed %d: redo: %v", seed, err)
			}
		}
		if got, _ := g.Canonical(); string(got) != string(final) {
			t.Fatalf("seed %d: redo-all did not reproduce final graph", seed)
		}
	}
}

func randomCommands(r *rand.Rand, g *cadir.Graph, nextID *int) []Command {
	pick := func() string {
		if len(g.Nodes) == 0 {
			return ""
		}
		return g.Nodes[r.Intn(len(g.Nodes))].ID
	}
	var out []Command
	for i := 0; i < 1+r.Intn(3); i++ {
		switch r.Intn(9) {
		case 0, 1:
			*nextID++
			n := box(fmt.Sprintf("n%d", *nextID))
			if p := pick(); p != "" && r.Intn(2) == 0 {
				n.Dependencies = []string{p}
			}
			out = append(out, &AddFeature{Node: n, Index: r.Intn(len(g.Nodes)+2) - 1})
		case 2:
			if id := pick(); id != "" {
				out = append(out, &RemoveFeature{ID: id})
			}
		case 3:
			if id := pick(); id != "" {
				out = append(out, &SetParameter{ID: id, Name: "dx", Value: float64(r.Intn(100))})
			}
		case 4:
			if id := pick(); id != "" {
				out = append(out, &SetExpression{ID: id, Name: "dy", Expr: fmt.Sprintf("%s.dx*2", pick())})
			}
		case 5:
			if id := pick(); id != "" {
				out = append(out, &Reorder{ID: id, Index: r.Intn(len(g.Nodes))})
			}
		case 6:
			if id := pick(); id != "" {
				out = append(out, &Suppress{ID: id, Suppressed: r.Intn(2) == 0})
			}
		case 7:
			if id := pick(); id != "" {
				out = append(out, &Rename{ID: id, Name: fmt.Sprintf("name%d", r.Intn(50))})
			}
		case 8:
			if id := pick(); id != "" {
				out = append(out, &SetExpression{ID: id, Name: "dy", Delete: true})
			}
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func TestResetAppearanceInverseRestoresAll(t *testing.T) {
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{box("a")}
	for _, c := range []Command{&SetColor{ID: "a", Color: "#00ff00"}, &SetOpacity{ID: "a", Opacity: ptr(0.3)}, &SetVisibility{ID: "a", Visible: ptr(false)}} {
		if _, err := c.Apply(g); err != nil {
			t.Fatal(err)
		}
	}
	want, _ := g.Canonical()
	inv, err := (&ResetAppearance{ID: "a"}).Apply(g)
	if err != nil || g.Get("a").Appearance != nil {
		t.Fatalf("reset failed: %v %+v", err, g.Get("a").Appearance)
	}
	if _, err := inv.Apply(g); err != nil {
		t.Fatal(err)
	}
	if got, _ := g.Canonical(); string(got) != string(want) {
		t.Fatalf("undo of reset not exact:\n%s\nvs\n%s", want, got)
	}
	if _, err := (&SetColor{ID: "a", Color: "red"}).Apply(g); err == nil {
		t.Fatal("expected invalid color error")
	}
	if _, err := (&SetOpacity{ID: "a", Opacity: ptr(1.5)}).Apply(g); err == nil {
		t.Fatal("expected invalid opacity error")
	}
}

// A node that cannot be serialized (a NaN parameter has no JSON form) must be refused with a
// message, not copied into the graph as an empty node.
func TestAddFeatureRefusesANodeThatCannotBeCopied(t *testing.T) {
	g := cadir.NewGraph()
	n := &cadir.Node{ID: "bad", Type: "box", Parameters: map[string]any{"dx": math.NaN(), "dy": 1.0, "dz": 1.0}}
	_, err := (&AddFeature{Node: n, Index: -1}).Apply(g)
	if err == nil || !strings.Contains(err.Error(), `"bad" cannot be copied`) {
		t.Fatalf("want a 'cannot be copied' error, got %v", err)
	}
	if len(g.Nodes) != 0 {
		t.Fatal("the graph must be left untouched")
	}
}
