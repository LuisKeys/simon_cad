package meshgeo

import (
	"math"
	"math/rand"
	"testing"
)

// unit-ish cube centred on the origin with side s, outward CCW triangles
func cubeMesh(s float32) ([]float32, []uint32) {
	h := s / 2
	p := []float32{
		-h, -h, -h, h, -h, -h, h, h, -h, -h, h, -h,
		-h, -h, h, h, -h, h, h, h, h, -h, h, h,
	}
	idx := []uint32{
		0, 2, 1, 0, 3, 2, 4, 5, 6, 4, 6, 7, 0, 1, 5, 0, 5, 4,
		2, 3, 7, 2, 7, 6, 1, 2, 6, 1, 6, 5, 3, 0, 4, 3, 4, 7,
	}
	return p, idx
}

func TestRayHitsCubeFaces(t *testing.T) {
	b := Build(cubeMesh(2))
	for _, c := range []struct {
		o, d Vec
		t    float64
		n    Vec
	}{
		{Vec{0, 0, 10}, Vec{0, 0, -1}, 9, Vec{0, 0, 1}},
		{Vec{10, 0.3, 0.2}, Vec{-1, 0, 0}, 9, Vec{1, 0, 0}},
		{Vec{0.1, -10, 0}, Vec{0, 1, 0}, 9, Vec{0, -1, 0}},
	} {
		h, ok := b.Raycast(c.o, c.d, 100)
		if !ok || math.Abs(h.T-c.t) > 1e-9 {
			t.Fatalf("%v: %+v ok=%v", c.o, h, ok)
		}
		for k := 0; k < 3; k++ {
			if math.Abs(h.Normal[k]-c.n[k]) > 1e-9 {
				t.Fatalf("normal %v want %v", h.Normal, c.n)
			}
		}
	}
	if _, ok := b.Raycast(Vec{0, 0, 10}, Vec{0, 0, 1}, 100); ok {
		t.Fatal("a ray pointing away must miss")
	}
	if _, ok := b.Raycast(Vec{5, 5, 10}, Vec{0, 0, -1}, 100); ok {
		t.Fatal("a ray beside the cube must miss")
	}
	if _, ok := b.Raycast(Vec{0, 0, 10}, Vec{0, 0, -1}, 5); ok {
		t.Fatal("tmax must limit the ray")
	}
	// from inside: hits the exit face
	if h, ok := b.Raycast(Vec{0, 0, 0}, Vec{0, 0, 1}, 100); !ok || math.Abs(h.T-1) > 1e-9 {
		t.Fatalf("inside: %+v", h)
	}
}

func TestClosestPoint(t *testing.T) {
	b := Build(cubeMesh(2))
	for _, c := range []struct {
		p    Vec
		want Vec
		d    float64
	}{
		{Vec{0, 0, 5}, Vec{0, 0, 1}, 4},
		{Vec{3, 0.5, 0.5}, Vec{1, 0.5, 0.5}, 2},
		{Vec{2, 2, 2}, Vec{1, 1, 1}, math.Sqrt(3)},
		{Vec{0, 0, 0}, Vec{}, 1},
	} {
		h, ok := b.Closest(c.p)
		if !ok || math.Abs(h.T-c.d) > 1e-9 {
			t.Fatalf("%v: %+v", c.p, h)
		}
		if c.want != (Vec{}) && norm(sub(h.Point, c.want)) > 1e-9 {
			t.Fatalf("%v: point %v want %v", c.p, h.Point, c.want)
		}
	}
}

// BVH answers must match brute force on a random mesh.
func TestMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	var pos []float32
	var idx []uint32
	for i := 0; i < 400; i++ {
		c := Vec{rng.Float64() * 20, rng.Float64() * 20, rng.Float64() * 20}
		for k := 0; k < 3; k++ {
			pos = append(pos, float32(c[0]+rng.Float64()*2), float32(c[1]+rng.Float64()*2), float32(c[2]+rng.Float64()*2))
			idx = append(idx, uint32(len(pos)/3-1))
		}
	}
	b := Build(pos, idx)
	for i := 0; i < 200; i++ {
		o := Vec{rng.Float64()*30 - 5, rng.Float64()*30 - 5, rng.Float64()*30 - 5}
		d := unit(Vec{rng.Float64() - .5, rng.Float64() - .5, rng.Float64() - .5})
		best, found := math.Inf(1), false
		for ti := 0; ti < b.Triangles(); ti++ {
			a, bb, c := b.Triangle(ti)
			if tt, ok := rayTri(o, d, tri{a, bb, c}); ok && tt < best {
				best, found = tt, true
			}
		}
		h, ok := b.Raycast(o, d, math.Inf(1))
		if ok != found || (ok && math.Abs(h.T-best) > 1e-9) {
			t.Fatalf("ray %d: bvh %v/%v brute %v/%v", i, h.T, ok, best, found)
		}
		cb := math.Inf(1)
		for ti := 0; ti < b.Triangles(); ti++ {
			a, bb, c := b.Triangle(ti)
			q := closestOnTri(o, tri{a, bb, c})
			cb = math.Min(cb, norm(sub(q, o)))
		}
		if ch, _ := b.Closest(o); math.Abs(ch.T-cb) > 1e-9 {
			t.Fatalf("closest %d: %v vs %v", i, ch.T, cb)
		}
	}
}

func TestEmptyAndDegenerate(t *testing.T) {
	b := Build(nil, nil)
	if _, ok := b.Raycast(Vec{}, Vec{0, 0, 1}, 10); ok {
		t.Fatal("empty mesh")
	}
	if _, ok := b.Closest(Vec{}); ok {
		t.Fatal("empty mesh")
	}
	d := Build([]float32{0, 0, 0, 1, 0, 0, 2, 0, 0}, []uint32{0, 1, 2}) // collinear: a sliver
	if _, ok := d.Raycast(Vec{1, 1, 1}, Vec{0, -1, 0}, 10); ok {
		t.Fatal("a degenerate triangle has no surface")
	}
}

func BenchmarkRaycast100k(b *testing.B) {
	rng := rand.New(rand.NewSource(2))
	var pos []float32
	var idx []uint32
	for i := 0; i < 100000; i++ {
		c := Vec{rng.Float64() * 100, rng.Float64() * 100, rng.Float64() * 100}
		for k := 0; k < 3; k++ {
			pos = append(pos, float32(c[0]+rng.Float64()), float32(c[1]+rng.Float64()), float32(c[2]+rng.Float64()))
			idx = append(idx, uint32(len(pos)/3-1))
		}
	}
	bv := Build(pos, idx)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bv.Raycast(Vec{rng.Float64() * 100, rng.Float64() * 100, -1}, Vec{0, 0, 1}, math.Inf(1))
	}
}
