// Package project drops 3D points and polylines onto the surface of a solid: along a direction
// (planar), to the nearest surface point (normal) or along a direction with adaptive refinement
// so the result hugs a curved surface (conform). It works on the solid's tessellation through
// the meshgeo BVH, so the accuracy is the mesh deflection; the order of the points is kept.
package project

import (
	"fmt"
	"github.com/accelone/simoncad/internal/vec"
	"math"

	"github.com/accelone/simoncad/internal/meshgeo"
)

// Vec is a 3D vector.
type Vec = [3]float64

// Surface is a triangle mesh prepared for queries.
type Surface struct {
	bvh      *meshgeo.BVH
	pos, nrm []float32
	idx      []uint32
	reach    float64 // longer than any ray needs to be
	lo, hi   Vec
}

// New indexes a mesh. normals (vertex normals, may be nil) smooth the reported surface normal;
// without them the face normal is used.
func New(positions, normals []float32, indices []uint32) (*Surface, error) {
	if len(indices) < 3 || len(positions) < 9 {
		return nil, fmt.Errorf("the target has no surface")
	}
	s := &Surface{bvh: meshgeo.Build(positions, indices), pos: positions, idx: indices}
	if len(normals) == len(positions) {
		s.nrm = normals
	}
	s.lo, s.hi = s.bvh.Bounds()
	s.reach = 2*math.Sqrt(sq(s.hi[0]-s.lo[0])+sq(s.hi[1]-s.lo[1])+sq(s.hi[2]-s.lo[2])) + 1
	return s, nil
}

func sq(x float64) float64 { return x * x }

func sub(a, b Vec) Vec         { return vec.Sub(a, b) }
func add(a, b Vec) Vec         { return vec.Add(a, b) }
func mul(a Vec, k float64) Vec { return vec.Scale(a, k) }
func dot(a, b Vec) float64     { return vec.Dot(a, b) }
func length(a Vec) float64     { return vec.Len(a) }
func unit(a Vec) Vec           { return vec.Unit(a) }
func cross(a, b Vec) Vec       { return vec.Cross(a, b) }

func (s *Surface) vert(i uint32) Vec {
	return Vec{float64(s.pos[3*i]), float64(s.pos[3*i+1]), float64(s.pos[3*i+2])}
}

// normalAt is the outward surface normal at p on triangle t: the barycentric blend of the vertex
// normals when the mesh has them, else the face normal (outward by winding).
func (s *Surface) normalAt(h meshgeo.Hit) Vec {
	if s.nrm == nil {
		return h.Normal
	}
	i0, i1, i2 := s.idx[3*h.Tri], s.idx[3*h.Tri+1], s.idx[3*h.Tri+2]
	a, b, c := s.vert(i0), s.vert(i1), s.vert(i2)
	n := cross(sub(b, a), sub(c, a))
	den := dot(n, n)
	if den < 1e-24 {
		return h.Normal
	}
	p := h.Point
	wa := dot(cross(sub(c, b), sub(p, b)), n) / den
	wb := dot(cross(sub(a, c), sub(p, c)), n) / den
	wc := 1 - wa - wb
	vn := func(i uint32) Vec { return Vec{float64(s.nrm[3*i]), float64(s.nrm[3*i+1]), float64(s.nrm[3*i+2])} }
	out := add(add(mul(vn(i0), wa), mul(vn(i1), wb)), mul(vn(i2), wc))
	if u := unit(out); u != (Vec{}) && dot(u, h.Normal) > 0 {
		return u
	}
	return h.Normal
}

// Planar casts a ray along dir through p and returns the surface point nearest to the side the
// ray comes from (the one you would see looking along dir) and its normal.
func (s *Surface) Planar(p, dir Vec) (Vec, Vec, bool) {
	d := unit(dir)
	if d == (Vec{}) {
		return Vec{}, Vec{}, false
	}
	origin := sub(p, mul(d, s.reach))
	h, ok := s.bvh.Raycast(origin, d, 2*s.reach)
	if !ok {
		return Vec{}, Vec{}, false
	}
	return h.Point, s.normalAt(h), true
}

// Nearest returns the surface point closest to p and its normal.
func (s *Surface) Nearest(p Vec) (Vec, Vec, bool) {
	h, ok := s.bvh.Closest(p)
	if !ok {
		return Vec{}, Vec{}, false
	}
	return h.Point, s.normalAt(h), true
}

// Mode is how points reach the surface.
type Mode string

const (
	ModePlanar  Mode = "planar"  // along Dir
	ModeNormal  Mode = "normal"  // to the nearest surface point
	ModeConform Mode = "conform" // planar, adding points where the chord leaves the surface
)

// Options configure a projection.
type Options struct {
	Mode   Mode
	Dir    Vec     // projection direction (planar, conform)
	Offset float64 // lift along the surface normal (mm), negative sinks into the surface
	Tol    float64 // conform: largest gap between a chord and the surface (default 0.05 mm)
}

// Result is a projected polyline.
type Result struct {
	Points  []Vec
	Normals []Vec
	// Source[i] is the index of the input point that Points[i] came from, or -1 for a point
	// inserted by conform refinement.
	Source []int
}

// FailError lists the input points that did not reach the surface.
type FailError struct{ Indices []int }

func (e *FailError) Error() string {
	n := len(e.Indices)
	if n > 8 {
		return fmt.Sprintf("%d points do not reach the surface (first: %v)", n, e.Indices[:8])
	}
	return fmt.Sprintf("points %v do not reach the surface", e.Indices)
}

func (s *Surface) one(p Vec, o Options) (Vec, Vec, bool) {
	var q, n Vec
	var ok bool
	if o.Mode == ModeNormal {
		q, n, ok = s.Nearest(p)
	} else {
		q, n, ok = s.Planar(p, o.Dir)
	}
	if !ok {
		return Vec{}, Vec{}, false
	}
	return add(q, mul(n, o.Offset)), n, true
}

// Project maps pts onto the surface. Planar and normal keep one output point per input point;
// conform may insert more. If any input point misses, the error is a *FailError naming them all.
func (s *Surface) Project(pts []Vec, o Options) (*Result, error) {
	switch o.Mode {
	case ModePlanar, ModeNormal, ModeConform:
	default:
		return nil, fmt.Errorf("mode must be planar, normal or conform (got %q)", o.Mode)
	}
	if o.Mode != ModeNormal && unit(o.Dir) == (Vec{}) {
		return nil, fmt.Errorf("planar and conform projection need a direction")
	}
	if o.Tol <= 0 {
		o.Tol = 0.05
	}
	res := &Result{}
	var fail []int
	type pp struct{ p, n Vec }
	got := make([]*pp, len(pts))
	for i, p := range pts {
		q, n, ok := s.one(p, o)
		if !ok {
			fail = append(fail, i)
			continue
		}
		got[i] = &pp{q, n}
	}
	if len(fail) > 0 {
		return nil, &FailError{fail}
	}
	for i := range pts {
		if i > 0 && o.Mode == ModeConform {
			s.refine(pts[i-1], pts[i], got[i-1].p, got[i].p, o, 0, res)
		}
		res.Points = append(res.Points, got[i].p)
		res.Normals = append(res.Normals, got[i].n)
		res.Source = append(res.Source, i)
	}
	return res, nil
}

const maxRefineDepth = 10

// refine inserts points between the projected a' and b' (sources a, b) while the projection of the
// source midpoint is farther than Tol from the chord a'b'. Points go in order into res.
func (s *Surface) refine(a, b, pa, pb Vec, o Options, depth int, res *Result) {
	if depth >= maxRefineDepth {
		return
	}
	m := mul(add(a, b), 0.5)
	pm, nm, ok := s.one(m, o)
	if !ok {
		return
	}
	chord := mul(add(pa, pb), 0.5)
	if length(sub(pm, chord)) <= o.Tol {
		return
	}
	s.refine(a, m, pa, pm, o, depth+1, res)
	res.Points = append(res.Points, pm)
	res.Normals = append(res.Normals, nm)
	res.Source = append(res.Source, -1)
	s.refine(m, b, pm, pb, o, depth+1, res)
}
