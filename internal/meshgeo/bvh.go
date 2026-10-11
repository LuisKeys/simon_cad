// Package meshgeo answers geometric queries on triangle meshes: ray casting and closest point,
// through a bounding-volume hierarchy. It serves surface projection (paths onto surfaces) and
// printability analysis (wall thickness) without any kernel dependency.
package meshgeo

import (
	"math"
	"sort"
)

// Vec is a 3D vector.
type Vec = [3]float64

func sub(a, b Vec) Vec         { return Vec{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func add(a, b Vec) Vec         { return Vec{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func mul(a Vec, s float64) Vec { return Vec{a[0] * s, a[1] * s, a[2] * s} }
func dot(a, b Vec) float64     { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func cross(a, b Vec) Vec {
	return Vec{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func norm(a Vec) float64 { return math.Sqrt(dot(a, a)) }
func unit(a Vec) Vec {
	n := norm(a)
	if n < 1e-300 {
		return Vec{}
	}
	return mul(a, 1/n)
}

type tri struct{ a, b, c Vec }

type node struct {
	lo, hi      Vec
	left, right int32 // children, or -1
	first, n    int32 // triangle range (leaf)
}

// BVH is an immutable acceleration structure over a triangle soup.
type BVH struct {
	tris  []tri
	order []int32 // triangle index (into the input) at each BVH slot
	nodes []node
}

// Hit is a ray or closest-point result.
type Hit struct {
	T      float64 // ray parameter (distance when dir is a unit vector); for Closest, the distance
	Tri    int     // triangle index in the input mesh
	Point  Vec
	Normal Vec // unit geometric normal of the triangle (winding order)
}

// Build indexes positions (xyz triples) and indices (triangle triples). Degenerate triangles are
// kept out of the hierarchy's way but keep their index slots.
func Build(positions []float32, indices []uint32) *BVH {
	n := len(indices) / 3
	b := &BVH{tris: make([]tri, n), order: make([]int32, 0, n)}
	cent := make([]Vec, n)
	p := func(i uint32) Vec {
		return Vec{float64(positions[3*i]), float64(positions[3*i+1]), float64(positions[3*i+2])}
	}
	for t := 0; t < n; t++ {
		tr := tri{p(indices[3*t]), p(indices[3*t+1]), p(indices[3*t+2])}
		b.tris[t] = tr
		cent[t] = mul(add(add(tr.a, tr.b), tr.c), 1.0/3)
		if norm(cross(sub(tr.b, tr.a), sub(tr.c, tr.a))) > 1e-18 {
			b.order = append(b.order, int32(t))
		}
	}
	if len(b.order) == 0 {
		return b
	}
	b.nodes = make([]node, 0, 2*len(b.order)/4+1)
	b.build(0, len(b.order), cent)
	return b
}

func (b *BVH) bounds(from, to int) (lo, hi Vec) {
	lo, hi = Vec{math.Inf(1), math.Inf(1), math.Inf(1)}, Vec{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, ti := range b.order[from:to] {
		t := b.tris[ti]
		for _, v := range [3]Vec{t.a, t.b, t.c} {
			for k := 0; k < 3; k++ {
				lo[k], hi[k] = math.Min(lo[k], v[k]), math.Max(hi[k], v[k])
			}
		}
	}
	return
}

func (b *BVH) build(from, to int, cent []Vec) int32 {
	lo, hi := b.bounds(from, to)
	idx := int32(len(b.nodes))
	b.nodes = append(b.nodes, node{lo: lo, hi: hi, left: -1, right: -1})
	if to-from <= 4 {
		b.nodes[idx].first, b.nodes[idx].n = int32(from), int32(to-from)
		return idx
	}
	ext := sub(hi, lo)
	axis := 0
	if ext[1] > ext[axis] {
		axis = 1
	}
	if ext[2] > ext[axis] {
		axis = 2
	}
	seg := b.order[from:to]
	sort.Slice(seg, func(i, j int) bool { return cent[seg[i]][axis] < cent[seg[j]][axis] })
	mid := (from + to) / 2
	l := b.build(from, mid, cent)
	r := b.build(mid, to, cent)
	b.nodes[idx].left, b.nodes[idx].right = l, r
	return idx
}

// Triangles is the number of input triangles.
func (b *BVH) Triangles() int { return len(b.tris) }

// rayBox is the slab test; returns whether the ray hits within [0, tmax].
func rayBox(o, invD Vec, lo, hi Vec, tmax float64) bool {
	t0, t1 := 0.0, tmax
	for k := 0; k < 3; k++ {
		a, c := (lo[k]-o[k])*invD[k], (hi[k]-o[k])*invD[k]
		if a > c {
			a, c = c, a
		}
		if math.IsNaN(a) || math.IsNaN(c) {
			continue
		}
		t0, t1 = math.Max(t0, a), math.Min(t1, c)
		if t0 > t1 {
			return false
		}
	}
	return true
}

// rayTri is Möller-Trumbore, double-sided; returns the distance t > eps.
func rayTri(o, d Vec, t tri) (float64, bool) {
	e1, e2 := sub(t.b, t.a), sub(t.c, t.a)
	p := cross(d, e2)
	det := dot(e1, p)
	if math.Abs(det) < 1e-14 {
		return 0, false
	}
	inv := 1 / det
	s := sub(o, t.a)
	u := dot(s, p) * inv
	if u < -1e-9 || u > 1+1e-9 {
		return 0, false
	}
	q := cross(s, e1)
	v := dot(d, q) * inv
	if v < -1e-9 || u+v > 1+1e-9 {
		return 0, false
	}
	tt := dot(e2, q) * inv
	return tt, tt > 1e-9
}

// Raycast returns the nearest intersection of the ray origin + t*dir (t in (0, tmax]) with the
// mesh. dir need not be a unit vector, but T is then in units of |dir|.
func (b *BVH) Raycast(origin, dir Vec, tmax float64) (Hit, bool) {
	if len(b.nodes) == 0 {
		return Hit{}, false
	}
	inv := Vec{1 / dir[0], 1 / dir[1], 1 / dir[2]}
	best := Hit{T: tmax}
	found := false
	stack := []int32{0}
	for len(stack) > 0 {
		ni := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := &b.nodes[ni]
		if !rayBox(origin, inv, n.lo, n.hi, best.T) {
			continue
		}
		if n.left < 0 {
			for _, ti := range b.order[n.first : n.first+n.n] {
				if t, ok := rayTri(origin, dir, b.tris[ti]); ok && t < best.T {
					tr := b.tris[ti]
					best = Hit{T: t, Tri: int(ti), Point: add(origin, mul(dir, t)), Normal: unit(cross(sub(tr.b, tr.a), sub(tr.c, tr.a)))}
					found = true
				}
			}
			continue
		}
		stack = append(stack, n.left, n.right)
	}
	return best, found
}

func boxDist2(p Vec, lo, hi Vec) float64 {
	d := 0.0
	for k := 0; k < 3; k++ {
		if p[k] < lo[k] {
			d += (lo[k] - p[k]) * (lo[k] - p[k])
		} else if p[k] > hi[k] {
			d += (p[k] - hi[k]) * (p[k] - hi[k])
		}
	}
	return d
}

// closestOnTri is the point of triangle t nearest to p (Ericson, Real-Time Collision Detection).
func closestOnTri(p Vec, t tri) Vec {
	ab, ac, ap := sub(t.b, t.a), sub(t.c, t.a), sub(p, t.a)
	d1, d2 := dot(ab, ap), dot(ac, ap)
	if d1 <= 0 && d2 <= 0 {
		return t.a
	}
	bp := sub(p, t.b)
	d3, d4 := dot(ab, bp), dot(ac, bp)
	if d3 >= 0 && d4 <= d3 {
		return t.b
	}
	if vc := d1*d4 - d3*d2; vc <= 0 && d1 >= 0 && d3 <= 0 {
		return add(t.a, mul(ab, d1/(d1-d3)))
	}
	cp := sub(p, t.c)
	d5, d6 := dot(ab, cp), dot(ac, cp)
	if d6 >= 0 && d5 <= d6 {
		return t.c
	}
	if vb := d5*d2 - d1*d6; vb <= 0 && d2 >= 0 && d6 <= 0 {
		return add(t.a, mul(ac, d2/(d2-d6)))
	}
	if va := d3*d6 - d5*d4; va <= 0 && d4-d3 >= 0 && d5-d6 >= 0 {
		return add(t.b, mul(sub(t.c, t.b), (d4-d3)/((d4-d3)+(d5-d6))))
	}
	denom := 1 / (d1*d4 - d3*d2 + 0) // fall through to the face interior
	_ = denom
	n := cross(ab, ac)
	return sub(p, mul(n, dot(sub(p, t.a), n)/dot(n, n)))
}

// Closest returns the point of the mesh nearest to p and its distance in Hit.T.
func (b *BVH) Closest(p Vec) (Hit, bool) {
	if len(b.nodes) == 0 {
		return Hit{}, false
	}
	best := Hit{T: math.Inf(1)}
	bestD2 := math.Inf(1)
	stack := []int32{0}
	for len(stack) > 0 {
		ni := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := &b.nodes[ni]
		if boxDist2(p, n.lo, n.hi) >= bestD2 {
			continue
		}
		if n.left < 0 {
			for _, ti := range b.order[n.first : n.first+n.n] {
				tr := b.tris[ti]
				q := closestOnTri(p, tr)
				if d2 := dot(sub(q, p), sub(q, p)); d2 < bestD2 {
					bestD2 = d2
					best = Hit{Tri: int(ti), Point: q, Normal: unit(cross(sub(tr.b, tr.a), sub(tr.c, tr.a)))}
				}
			}
			continue
		}
		// visit the nearer child last so it is popped first
		l, r := &b.nodes[n.left], &b.nodes[n.right]
		if boxDist2(p, l.lo, l.hi) < boxDist2(p, r.lo, r.hi) {
			stack = append(stack, n.right, n.left)
		} else {
			stack = append(stack, n.left, n.right)
		}
	}
	best.T = math.Sqrt(bestD2)
	return best, !math.IsInf(bestD2, 1)
}

// Triangle returns the corners of input triangle i.
func (b *BVH) Triangle(i int) (Vec, Vec, Vec) { t := b.tris[i]; return t.a, t.b, t.c }

// Bounds is the axis-aligned box of the whole mesh.
func (b *BVH) Bounds() (lo, hi Vec) {
	if len(b.nodes) == 0 {
		return
	}
	return b.nodes[0].lo, b.nodes[0].hi
}
