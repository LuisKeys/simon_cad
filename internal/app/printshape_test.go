package app

import (
	"strings"
	"testing"
)

func TestPrintShapeIsAReadOnlyCopy(t *testing.T) {
	a := New("p", clock)
	defer a.Close()
	if _, _, err := a.PrintShape(); err == nil || !strings.Contains(err.Error(), "no solid geometry") {
		t.Fatalf("empty model: %v", err)
	}
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":20,"dy":10,"dz":5}`), PolicyAuto))
	if _, err := a.Model(); err != nil { // warm the rebuild cache so the shape count is stable
		t.Fatal(err)
	}
	live, hash0 := a.K.LiveShapes(), a.Graph.GeometryHash()
	graph0 := a.Graph.Hash()

	sh, h, err := a.PrintShape()
	if err != nil {
		t.Fatal(err)
	}
	if h != hash0 {
		t.Fatalf("revision %q, want the graph's geometry hash %q", h, hash0)
	}
	props, err := sh.Props()
	if err != nil || !near(props.Volume, 1000) {
		t.Fatalf("volume %v %v", props.Volume, err)
	}
	if a.K.LiveShapes() != live+1 {
		t.Fatal("PrintShape must hand out exactly one new shape")
	}
	sh.Release()
	if a.K.LiveShapes() != live {
		t.Fatalf("releasing the copy must restore the shape count: %d vs %d", a.K.LiveShapes(), live)
	}
	if a.Graph.Hash() != graph0 {
		t.Fatal("PrintShape changed the model")
	}
	// an appearance-only change keeps the geometry revision (so a slice stays valid)
	mustOK(t, a.ApplyTool("set_color", []byte(`{"features":["b"],"color":"#ff0000"}`), PolicyAuto))
	if sh2, h2, err := a.PrintShape(); err != nil || h2 != hash0 {
		t.Fatalf("colour must not change the geometry revision: %v %v", h2, err)
	} else {
		sh2.Release()
	}
}
