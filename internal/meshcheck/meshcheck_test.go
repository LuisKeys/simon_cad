package meshcheck

import (
	"math"
	"testing"
)

// unit cube, outward CCW triangles
func cube(s float32, ox float32) ([]float32, []uint32) {
	p := []float32{
		0, 0, 0, s, 0, 0, s, s, 0, 0, s, 0,
		0, 0, s, s, 0, s, s, s, s, 0, s, s,
	}
	for i := 0; i < len(p); i += 3 {
		p[i] += ox
	}
	idx := []uint32{
		0, 2, 1, 0, 3, 2, // bottom
		4, 5, 6, 4, 6, 7, // top
		0, 1, 5, 0, 5, 4, // front
		2, 3, 7, 2, 7, 6, // back
		1, 2, 6, 1, 6, 5, // right
		3, 0, 4, 3, 4, 7, // left
	}
	return p, idx
}

func TestWatertightCube(t *testing.T) {
	p, i := cube(2, 0)
	r := Check(p, i)
	if !r.Watertight() || r.Components != 1 || r.Triangles != 12 {
		t.Fatalf("%+v", r)
	}
	if math.Abs(r.SignedVolume-8) > 1e-9 {
		t.Fatalf("volume %v", r.SignedVolume)
	}
	if r.Max[0] != 2 || r.Min[0] != 0 {
		t.Fatalf("bbox %v %v", r.Min, r.Max)
	}
}

func TestOpenAndFlippedAndComponents(t *testing.T) {
	p, i := cube(2, 0)
	open := Check(p, i[:len(i)-3]) // drop one triangle
	if open.Watertight() || open.OpenEdges != 3 {
		t.Fatalf("open %+v", open)
	}
	p2, i2 := cube(2, 10)
	base := uint32(len(p) / 3)
	for _, x := range i2 {
		i = append(i, x+base)
	}
	two := Check(append(p, p2...), i)
	if two.Components != 2 || !two.Watertight() {
		t.Fatalf("two cubes %+v", two)
	}
	// a degenerate triangle is ignored, not counted as open
	pd := append(append([]float32{}, p...), 5, 5, 5)
	idd := append(append([]uint32{}, i[:36]...), 0, 0, uint32(len(p)/3))
	d := Check(pd, idd)
	if d.Degenerate != 1 || !d.Watertight() {
		t.Fatalf("degenerate %+v", d)
	}
}

func TestNonManifoldEdge(t *testing.T) {
	// three triangles sharing one edge
	p := []float32{0, 0, 0, 1, 0, 0, 0, 1, 0, 0, -1, 0, 0, 0, 1}
	i := []uint32{0, 1, 2, 0, 3, 1, 0, 1, 4}
	if r := Check(p, i); r.NonManifold != 1 || r.Watertight() {
		t.Fatalf("%+v", r)
	}
}
