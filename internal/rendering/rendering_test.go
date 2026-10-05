package rendering_test

import (
	"strings"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/rendering"
	"github.com/accelone/simoncad/internal/selectors"
)

var clock = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func run(t *testing.T, a *app.App, tool, args string) {
	t.Helper()
	if o := a.ApplyTool(tool, []byte(args), app.PolicyCommit); !o.OK {
		t.Fatalf("%s: %s", tool, o.Error)
	}
}

// A plate with a pocket and four holes: pick IDs and selectors must map both ways for
// every face, edge and vertex, and a pocket floor must not collapse onto the top face.
func TestPickSelectorBijection(t *testing.T) {
	a := app.New("t", clock)
	defer a.Close()
	run(t, a, "create_box", `{"id":"plate","dx":60,"dy":40,"dz":6}`)
	run(t, a, "extrude_profile", `{"id":"pocket","target":"plate","mode":"cut","profile":"rectangle","x":15,"y":10,"w":30,"h":20,"depth":3,"offset":3}`)
	run(t, a, "add_hole", `{"id":"holes","target":"pocket","diameter":3.2,"positions":[[5,5],[55,5],[5,35],[55,35]]}`)
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	r := rendering.New()
	d, err := r.Scene(m, nil)
	if err != nil || len(d.Changed) != 1 {
		t.Fatalf("scene: %v, %d bodies", err, len(d.Changed))
	}
	b := d.Changed[0]
	if b.ID != "holes" {
		t.Fatalf("visible body %s, want the final feature", b.ID)
	}
	seen := map[string]int{}
	for i := range b.Faces {
		sel, err := b.SelectorFor(rendering.PickFace, i)
		if err != nil {
			t.Fatal(err)
		}
		if j, dup := seen[sel]; dup {
			t.Fatalf("faces %d and %d share selector %s", j, i, sel)
		}
		seen[sel] = i
		p, err := b.PickFor(sel)
		if err != nil || p.Kind != rendering.PickFace || p.Index != i {
			t.Fatalf("face %d → %s → %+v (%v)", i, sel, p, err)
		}
	}
	for i := range b.Edges {
		sel, _ := b.SelectorFor(rendering.PickEdge, i)
		if p, err := b.PickFor(sel); err != nil || p.Index != i {
			t.Fatalf("edge %d → %s → %+v (%v)", i, sel, p, err)
		}
	}
	for i := range b.Vertices {
		sel, _ := b.SelectorFor(rendering.PickVertex, i)
		if p, err := b.PickFor(sel); err != nil || p.Index != i {
			t.Fatalf("vertex %d → %s → %+v (%v)", i, sel, p, err)
		}
	}
	// The pocket floor (z=3, facing +Z) is its own semantic face, distinct from the top.
	floor, top := "", ""
	for _, f := range b.Faces {
		if f.Normal != nil && f.Normal[2] == 1 {
			if f.Centroid[2] == 3 {
				floor = f.Selector
			} else if f.Centroid[2] == 6 {
				top = f.Selector
			}
		}
	}
	if !strings.Contains(floor, "side=+Z") || !strings.Contains(floor, "at=3") || top != "face(feature=holes,side=+Z)" {
		t.Fatalf("floor %q top %q", floor, top)
	}
	// Triangles map back to faces.
	for i, f := range b.Faces {
		if got, ok := b.FaceOfTriangle(f.TriStart + f.TriCount - 1); !ok || got != i {
			t.Fatalf("triangle of face %d resolved to %d", i, got)
		}
	}
	// Cylindrical hole walls are reachable by index and describe themselves as cylinders.
	cyl := 0
	for _, f := range b.Faces {
		if f.Surface == "cylinder" {
			cyl++
			if !strings.Contains(f.Selector, "index=") {
				t.Fatalf("cylinder selector %q", f.Selector)
			}
		}
	}
	if cyl < 4 {
		t.Fatalf("expected the four hole walls, got %d cylinders", cyl)
	}
}

// Only changed bodies are re-meshed and resent.
func TestSceneDeltaOnlyResendsChangedBodies(t *testing.T) {
	a := app.New("t", clock)
	defer a.Close()
	run(t, a, "create_box", `{"id":"base","dx":40,"dy":30,"dz":4}`)
	run(t, a, "create_cylinder", `{"id":"post","radius":3,"height":20}`)
	r := rendering.New()
	m, _ := a.Model()
	d1, err := r.Scene(m, nil)
	if err != nil || len(d1.Changed) != 2 || d1.Stats.Tessellated != 2 {
		t.Fatalf("first scene: %v %+v", err, d1.Stats)
	}
	m, _ = a.Model()
	d2, _ := r.Scene(m, d1.Keys)
	if len(d2.Changed) != 0 || d2.Stats.Tessellated != 0 || d2.Stats.Reused != 2 {
		t.Fatalf("unchanged model resent geometry: %+v changed=%d", d2.Stats, len(d2.Changed))
	}
	run(t, a, "modify_parameter", `{"feature":"post","parameter":"height","value":25}`)
	m, _ = a.Model()
	d3, _ := r.Scene(m, d2.Keys)
	if len(d3.Changed) != 1 || d3.Changed[0].ID != "post" || d3.Stats.Tessellated != 1 {
		t.Fatalf("edit should resend only the post: %+v", d3.Stats)
	}
	run(t, a, "remove_feature", `{"feature":"post"}`)
	m, _ = a.Model()
	d4, _ := r.Scene(m, d3.Keys)
	if len(d4.Removed) != 1 || d4.Removed[0] != "post" {
		t.Fatalf("removed %v", d4.Removed)
	}
	if n := a.K.LiveMeshes(); n != 0 {
		t.Fatalf("native meshes leaked: %d", n)
	}
}

func TestSelectorErrors(t *testing.T) {
	a := app.New("t", clock)
	defer a.Close()
	run(t, a, "create_box", `{"id":"b","dx":10,"dy":10,"dz":10}`)
	m, _ := a.Model()
	d, _ := rendering.New().Scene(m, nil)
	b := d.Changed[0]
	if _, err := b.SelectorFor(rendering.PickFace, 99); err == nil {
		t.Fatal("out-of-range pick must fail")
	}
	if _, err := b.PickFor("face(feature=other,side=+Z)"); err == nil {
		t.Fatal("selector on another body must fail")
	}
	if _, err := selectors.ResolveTopo(selectors.Selector{Kind: "face", Args: map[string]string{"feature": "b", "side": "+Z", "at": "5"}}, b.Topology()); err == nil {
		t.Fatal("no face lies at z=5")
	}
}
