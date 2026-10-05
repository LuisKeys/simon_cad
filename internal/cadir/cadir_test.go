package cadir

import (
	"bytes"
	"encoding/json"
	"testing"
)

func sample() *Graph {
	g := NewGraph()
	g.Nodes = []*Node{
		{ID: "plate", Type: "box", Parameters: map[string]any{"dz": 4.0, "dx": 80.0, "dy": 60.0}, Provenance: Provenance{CreatedBy: "agent", ChangeSet: "cs_0001"}},
		{ID: "h", Type: "hole", Dependencies: []string{"plate"}, Expressions: map[string]string{"depth": "plate.dz*0.5"},
			References: map[string]string{"face": "face(feature=plate,side=+Z)"}},
	}
	return g
}

func TestCanonicalIsByteStableAcrossRoundTrip(t *testing.T) {
	a, _ := sample().Canonical()
	var g Graph
	if err := json.Unmarshal(a, &g); err != nil {
		t.Fatal(err)
	}
	b, _ := g.Canonical()
	if !bytes.Equal(a, b) {
		t.Fatalf("round trip changed bytes:\n%s\n%s", a, b)
	}
}

func TestHashIgnoresDerivedAndTimestamps(t *testing.T) {
	g := sample()
	h := g.Hash()
	g.Nodes[0].Status, g.Nodes[0].Errors = "failed", []string{"x"}
	g.Nodes[0].Metadata.Modified = "2030-01-01T00:00:00Z"
	if g.Hash() != h {
		t.Fatal("hash must ignore status/errors/timestamps")
	}
	g.Nodes[0].Parameters["dz"] = 5.0
	if g.Hash() == h {
		t.Fatal("hash must change with parameters")
	}
}

func TestEffectiveDepsIncludeExpressionsAndSelectors(t *testing.T) {
	g := sample()
	n := &Node{ID: "x", Type: "hole", Expressions: map[string]string{"depth": "plate.dz + h.depth"},
		References: map[string]string{"face": "face(feature=other,side=+Z)"}}
	got := EffectiveDeps(n)
	want := []string{"plate", "h", "other"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("deps %v", got)
	}
	_ = g
}

func TestValidateRejectsCyclesAndMissing(t *testing.T) {
	g := sample()
	g.Nodes[0].Dependencies = []string{"h"}
	if g.Validate() == nil {
		t.Fatal("cycle")
	}
	g = sample()
	g.Nodes[1].Dependencies = []string{"ghost"}
	if g.Validate() == nil {
		t.Fatal("missing dependency")
	}
	g = sample()
	g.Nodes[1].Expressions["depth"] = "1 +"
	if g.Validate() == nil {
		t.Fatal("bad expression")
	}
}
