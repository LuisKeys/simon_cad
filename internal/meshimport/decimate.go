package meshimport

import (
	"container/heap"
	"math"
)

// MinDecimateTarget is the fewest triangles a decimation may ask for.
const MinDecimateTarget = 12

// quad is a symmetric 4x4 quadric (Garland & Heckbert): the sum of squared distances to a set of
// planes, as a polynomial in the point. Fields are a², ab, ac, ad, b², bc, bd, c², cd, d².
type quad [10]float64

func planeQuad(n vec, d, w float64) quad {
	return quad{w * n[0] * n[0], w * n[0] * n[1], w * n[0] * n[2], w * n[0] * d,
		w * n[1] * n[1], w * n[1] * n[2], w * n[1] * d,
		w * n[2] * n[2], w * n[2] * d, w * d * d}
}

func (q quad) add(o quad) quad {
	for i := range q {
		q[i] += o[i]
	}
	return q
}

func (q quad) eval(p vec) float64 {
	return q[0]*p[0]*p[0] + 2*q[1]*p[0]*p[1] + 2*q[2]*p[0]*p[2] + 2*q[3]*p[0] +
		q[4]*p[1]*p[1] + 2*q[5]*p[1]*p[2] + 2*q[6]*p[1] +
		q[7]*p[2]*p[2] + 2*q[8]*p[2] + q[9]
}

// optimal is the point minimising the quadric, when the system is well conditioned.
func (q quad) optimal() (vec, bool) {
	a := [3][3]float64{{q[0], q[1], q[2]}, {q[1], q[4], q[5]}, {q[2], q[5], q[7]}}
	b := vec{-q[3], -q[6], -q[8]}
	det := a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) - a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) + a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
	scale := math.Abs(a[0][0]) + math.Abs(a[1][1]) + math.Abs(a[2][2])
	if scale == 0 || math.Abs(det) < 1e-12*scale*scale*scale {
		return vec{}, false
	}
	inv := func(i, j int) float64 { // cofactor
		r, c := [2]int{(i + 1) % 3, (i + 2) % 3}, [2]int{(j + 1) % 3, (j + 2) % 3}
		return a[r[0]][c[0]]*a[r[1]][c[1]] - a[r[0]][c[1]]*a[r[1]][c[0]]
	}
	var p vec
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			p[i] += inv(j, i) * b[j] / det
		}
	}
	return p, !(math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsNaN(p[2]))
}

type collapse struct {
	cost   float64
	u, v   int32
	pos    vec
	vu, vv int32 // vertex versions when queued: a stale entry is dropped
}

type collapseHeap []collapse

func (h collapseHeap) Len() int           { return len(h) }
func (h collapseHeap) Less(i, j int) bool { return h[i].cost < h[j].cost }
func (h collapseHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *collapseHeap) Push(x any)        { *h = append(*h, x.(collapse)) }
func (h *collapseHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// minTurn is the cosine below which a collapse is refused because it would turn a neighbouring
// triangle over (or nearly): the surface would fold.
const minTurn = 0.2

// Decimate reduces a closed manifold mesh to about target triangles by collapsing the edges that
// change the surface least (quadric error metric). Collapses that would change the topology (the
// link condition) or fold a triangle are never made, so a closed, consistently oriented manifold
// stays one; it can still end up crossing itself, which Validate catches. It returns the mesh
// and how many triangles it reached, which is above target when no further edge could be
// collapsed safely. m is not changed.
func Decimate(m *Mesh, target int) (*Mesh, error) {
	if target < MinDecimateTarget {
		return nil, reject("a mesh cannot be reduced below %d triangles", MinDecimateTarget)
	}
	nv, nt := len(m.Positions)/3, m.Triangles()
	if target >= nt {
		return nil, reject("the mesh has %d triangles already, not more than the %d asked for", nt, target)
	}
	pos := make([]vec, nv)
	for i := range pos {
		pos[i] = m.vertex(uint32(i))
	}
	tris := make([][3]int32, nt)
	alive := make([]bool, nt)
	vtris := make([][]int32, nv)
	q := make([]quad, nv)
	for t := 0; t < nt; t++ {
		tris[t] = [3]int32{int32(m.Indices[3*t]), int32(m.Indices[3*t+1]), int32(m.Indices[3*t+2])}
		alive[t] = true
		a, b, c := pos[tris[t][0]], pos[tris[t][1]], pos[tris[t][2]]
		n := b.sub(a).cross(c.sub(a))
		area := n.norm() / 2
		if area == 0 {
			continue
		}
		n = vec{n[0] / (2 * area), n[1] / (2 * area), n[2] / (2 * area)}
		pq := planeQuad(n, -n.dot(a), area)
		for _, v := range tris[t] {
			vtris[v] = append(vtris[v], int32(t))
			q[v] = q[v].add(pq)
		}
	}
	vAlive := make([]bool, nv)
	for i := range vAlive {
		vAlive[i] = len(vtris[i]) > 0
	}
	ver := make([]int32, nv)
	left := nt

	// incident returns the live triangles of v, dropping dead ones from its list.
	incident := func(v int32) []int32 {
		list := vtris[v][:0]
		for _, t := range vtris[v] {
			if alive[t] && (tris[t][0] == v || tris[t][1] == v || tris[t][2] == v) {
				list = append(list, t)
			}
		}
		vtris[v] = list
		return list
	}
	neighbours := func(v int32) map[int32]bool {
		s := map[int32]bool{}
		for _, t := range incident(v) {
			for _, w := range tris[t] {
				if w != v {
					s[w] = true
				}
			}
		}
		return s
	}
	contains := func(t int32, v int32) bool { return tris[t][0] == v || tris[t][1] == v || tris[t][2] == v }

	// legal reports whether collapsing u and v into p keeps the surface a manifold without folds.
	legal := func(u, v int32, p vec) bool {
		nu, nvv := neighbours(u), neighbours(v)
		common, shared := 0, 0
		for w := range nu {
			if nvv[w] {
				common++
			}
		}
		for _, t := range incident(u) {
			if contains(t, v) {
				shared++
			}
		}
		if shared != 2 || common != 2 { // link condition: the edge's two triangles are the only meeting
			return false
		}
		for _, end := range [2]int32{u, v} {
			for _, t := range incident(end) {
				if contains(t, u) && contains(t, v) {
					continue
				}
				var before, after [3]vec
				for k, w := range tris[t] {
					before[k], after[k] = pos[w], pos[w]
					if w == u || w == v {
						after[k] = p
					}
				}
				n0 := before[1].sub(before[0]).cross(before[2].sub(before[0]))
				n1 := after[1].sub(after[0]).cross(after[2].sub(after[0]))
				l0, l1 := n0.norm(), n1.norm()
				if l0 == 0 || l1 < 1e-9*l0 || n0.dot(n1) < minTurn*l0*l1 {
					return false
				}
			}
		}
		return true
	}
	// candidate prices the collapse of edge u-v: the best of the quadric's optimum, the two ends
	// and the middle.
	candidate := func(u, v int32) collapse {
		qq := q[u].add(q[v])
		mid := vec{(pos[u][0] + pos[v][0]) / 2, (pos[u][1] + pos[v][1]) / 2, (pos[u][2] + pos[v][2]) / 2}
		best, cost := mid, qq.eval(mid)
		try := func(p vec) {
			if c := qq.eval(p); c < cost {
				best, cost = p, c
			}
		}
		try(pos[u])
		try(pos[v])
		if p, ok := qq.optimal(); ok {
			// An optimum far from the edge is an artefact of a thin quadric, not a better place.
			if p.sub(mid).norm() <= 2*pos[u].sub(pos[v]).norm() {
				try(p)
			}
		}
		return collapse{cost: math.Max(cost, 0), u: u, v: v, pos: best, vu: ver[u], vv: ver[v]}
	}

	h := &collapseHeap{}
	seen := map[[2]int32]bool{}
	for t := 0; t < nt; t++ {
		for j := 0; j < 3; j++ {
			a, b := tris[t][j], tris[t][(j+1)%3]
			if a > b {
				a, b = b, a
			}
			if !seen[[2]int32{a, b}] {
				seen[[2]int32{a, b}] = true
				*h = append(*h, candidate(a, b))
			}
		}
	}
	heap.Init(h)

	for left > target && h.Len() > 0 {
		c := heap.Pop(h).(collapse)
		if !vAlive[c.u] || !vAlive[c.v] || ver[c.u] != c.vu || ver[c.v] != c.vv {
			continue
		}
		if !legal(c.u, c.v, c.pos) {
			continue
		}
		// v merges into u.
		for _, t := range incident(c.v) {
			if contains(t, c.u) {
				alive[t] = false
				left--
				continue
			}
			for k := range tris[t] {
				if tris[t][k] == c.v {
					tris[t][k] = c.u
				}
			}
			vtris[c.u] = append(vtris[c.u], t)
		}
		pos[c.u] = c.pos
		q[c.u] = q[c.u].add(q[c.v])
		vAlive[c.v] = false
		ver[c.u]++
		for w := range neighbours(c.u) {
			a, b := c.u, w
			if a > b {
				a, b = b, a
			}
			heap.Push(h, candidate(a, b))
		}
	}

	// Compact: live triangles, only the vertices they use, in first-use order.
	out := &Mesh{}
	remap := map[int32]uint32{}
	for t := 0; t < nt; t++ {
		if !alive[t] {
			continue
		}
		for _, v := range tris[t] {
			id, ok := remap[v]
			if !ok {
				id = uint32(len(out.Positions) / 3)
				remap[v] = id
				out.Positions = append(out.Positions, float32(pos[v][0]), float32(pos[v][1]), float32(pos[v][2]))
			}
			out.Indices = append(out.Indices, id)
		}
	}
	return out, nil
}
