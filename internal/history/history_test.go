package history

import (
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
)

func TestCheckpointsFindAndCompare(t *testing.T) {
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{{ID: "plate", Type: "box", Parameters: map[string]any{"dz": 4.0}}}
	var l Log
	now := time.Unix(0, 0)
	c1 := l.Create("Created plate", "agent", []string{"cs_0001"}, g, now)
	g.Nodes[0].Parameters["dz"] = 5.0
	g.Nodes = append(g.Nodes, &cadir.Node{ID: "h", Type: "hole"})
	c2 := l.Create("Reinforced shoulder", "agent", []string{"cs_0002"}, g, now)
	if c1.GraphHash == c2.GraphHash {
		t.Fatal("hash should differ")
	}
	for ref, want := range map[string]string{"cp_001": c1.ID, "2": c2.ID, "reinforced": c2.ID, "previous": c1.ID, "last": c2.ID} {
		got, err := l.Find(ref)
		if err != nil || got.ID != want {
			t.Errorf("Find(%q) = %v, %v; want %s", ref, got, err, want)
		}
	}
	if _, err := l.Find("nothing"); err == nil {
		t.Error("expected miss")
	}
	d := Compare(c1.Snapshot, c2.Snapshot)
	if len(d.Added) != 1 || d.Added[0] != "h" || len(d.Changed) != 1 || d.Changed[0].To != 5.0 {
		t.Fatalf("diff %+v", d)
	}
	if !Compare(c1.Snapshot, c1.Snapshot).Empty() {
		t.Fatal("self-diff must be empty")
	}
}
