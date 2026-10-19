package meshimport

import (
	"math"
	"sort"
)

// Limits of the automatic hole closing. Past them the file is more likely to be missing a whole
// part of the model than to have a scanning gap, and inventing that part would be wrong.
const (
	// MaxHoleEdges bounds the boundary length of one hole.
	MaxHoleEdges = 3000
	// maxPatchArea bounds the surface the patches add, as a fraction of the surface the mesh has.
	maxPatchArea = 0.5
)

// FillReport says what closing the holes did.
type FillReport struct {
	Holes     int `json:"holes"`
	Triangles int `json:"triangles"` // added
	Largest   int `json:"largest"`   // boundary edges of the biggest hole
}

// FillHoles closes the holes of m in place: every loop of open edges becomes a flat-ish patch of
// triangles that keeps the surface's orientation. It only adds triangles (and, for a hole it
// cannot triangulate any other way, one vertex at its centroid); the rest of the mesh is
// untouched. It does not decide whether the result is a good solid: Validate still does, and a
// patch that crosses the model is refused there.
//
// maxTris is the triangle limit the mesh must still respect after the patches.
func FillHoles(m *Mesh, maxTris int) (FillReport, error) {
	var rep FillReport
	type edge = [2]uint32
	dir := make(map[edge]bool, m.Triangles()*3)
	for t := 0; t < m.Triangles(); t++ {
		for j := 0; j < 3; j++ {
			a, b := m.Indices[3*t+j], m.Indices[3*t+(j+1)%3]
			if a != b {
				dir[edge{a, b}] = true
			}
		}
	}
	// A patch walks each open edge backwards: the open edge a->b gets the patch edge b->a.
	out := map[uint32][]uint32{}
	for e := range dir {
		if !dir[edge{e[1], e[0]}] {
			out[e[1]] = append(out[e[1]], e[0])
		}
	}
	if len(out) == 0 {
		return rep, nil
	}
	for v := range out {
		sort.Slice(out[v], func(i, j int) bool { return out[v][i] < out[v][j] }) // deterministic
	}
	starts := make([]uint32, 0, len(out))
	for v := range out {
		starts = append(starts, v)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })

	surface := area(m, m.Indices)

	// Walk the patch edges into closed loops. A vertex where two holes touch has several ways on:
	// any choice gives closed walks, and a walk that comes back to a vertex it already passed
	// is cut there, so every loop is a simple polygon.
	var loops [][]uint32
	for _, s := range starts {
		for len(out[s]) > 0 {
			path, pos := []uint32{s}, map[uint32]int{s: 0}
			for {
				v := path[len(path)-1]
				ways := out[v]
				if len(ways) == 0 {
					return rep, reject("a hole near %s cannot be closed: its border does not form a loop", at(m.vertex(v)))
				}
				t := ways[0]
				out[v] = ways[1:]
				if i, seen := pos[t]; seen {
					loops = append(loops, append([]uint32(nil), path[i:]...))
					for _, w := range path[i+1:] {
						delete(pos, w)
					}
					path = path[:i+1]
					if len(path) == 1 && len(out[path[0]]) == 0 {
						break
					}
					continue
				}
				pos[t] = len(path)
				path = append(path, t)
			}
		}
	}

	exists := func(a, b uint32) bool { return dir[edge{a, b}] || dir[edge{b, a}] }
	added := 0
	var tris []uint32
	for _, loop := range loops {
		if len(loop) < 3 {
			return rep, reject("a hole near %s cannot be closed: its border has only %d edges", at(m.vertex(loop[0])), len(loop))
		}
		if len(loop) > MaxHoleEdges {
			return rep, reject("a hole near %s has %d border edges, more than the %d that are closed automatically", at(m.vertex(loop[0])), len(loop), MaxHoleEdges)
		}
		patch := earClip(m, loop, exists)
		if patch == nil { // no ears: a fan from the centroid always works for a loop that is not wildly concave
			c := m.vertex(loop[0])
			for _, v := range loop[1:] {
				p := m.vertex(v)
				c = vec{c[0] + p[0], c[1] + p[1], c[2] + p[2]}
			}
			n := float64(len(loop))
			id := uint32(len(m.Positions) / 3)
			m.Positions = append(m.Positions, float32(c[0]/n), float32(c[1]/n), float32(c[2]/n))
			for i, v := range loop {
				patch = append(patch, v, loop[(i+1)%len(loop)], id)
			}
		}
		for i := 0; i < len(patch); i += 3 {
			dir[edge{patch[i], patch[i+1]}], dir[edge{patch[i+1], patch[i+2]}], dir[edge{patch[i+2], patch[i]}] = true, true, true
		}
		tris = append(tris, patch...)
		added += len(patch) / 3
		rep.Holes++
		rep.Largest = max(rep.Largest, len(loop))
	}
	if pa := area(m, tris); pa > maxPatchArea*surface {
		return FillReport{}, reject("the holes are too big to close: the patches would add %.0f%% to the model's surface, so the file is probably missing a whole part, not a gap", 100*pa/surface)
	}
	if m.Triangles()+added > maxTris {
		return FillReport{}, reject("closing the holes would take the mesh to %d triangles, the limit is %d", m.Triangles()+added, maxTris)
	}
	m.Indices = append(m.Indices, tris...)
	rep.Triangles = added
	return rep, nil
}

// area is the total area of the triangles in idx.
func area(m *Mesh, idx []uint32) float64 {
	var a float64
	for i := 0; i+2 < len(idx); i += 3 {
		p, q, r := m.vertex(idx[i]), m.vertex(idx[i+1]), m.vertex(idx[i+2])
		a += q.sub(p).cross(r.sub(p)).norm() / 2
	}
	return a
}

func loopExtent(m *Mesh, loop []uint32) float64 {
	var lo, hi vec
	for k := 0; k < 3; k++ {
		lo[k], hi[k] = math.Inf(1), math.Inf(-1)
	}
	for _, v := range loop {
		p := m.vertex(v)
		for k := 0; k < 3; k++ {
			lo[k], hi[k] = math.Min(lo[k], p[k]), math.Max(hi[k], p[k])
		}
	}
	return vec{hi[0] - lo[0], hi[1] - lo[1], hi[2] - lo[2]}.norm()
}

// earClip triangulates a loop by cutting off one ear at a time in the loop's mean plane, keeping
// the loop's winding. A diagonal that is already an edge of the mesh is skipped (it would be used
// by three triangles). It returns nil when no triangulation was found.
func earClip(m *Mesh, loop []uint32, exists func(a, b uint32) bool) []uint32 {
	n := len(loop)
	// Newell's normal: the area-weighted normal of the loop, oriented with its winding.
	var nrm vec
	for i := 0; i < n; i++ {
		p, q := m.vertex(loop[i]), m.vertex(loop[(i+1)%n])
		nrm[0] += (p[1] - q[1]) * (p[2] + q[2])
		nrm[1] += (p[2] - q[2]) * (p[0] + q[0])
		nrm[2] += (p[0] - q[0]) * (p[1] + q[1])
	}
	l := nrm.norm()
	if l == 0 {
		return nil
	}
	nrm = vec{nrm[0] / l, nrm[1] / l, nrm[2] / l}
	ref := vec{1, 0, 0}
	if math.Abs(nrm[0]) > 0.9 {
		ref = vec{0, 1, 0}
	}
	u := nrm.cross(ref)
	u = vec{u[0] / u.norm(), u[1] / u.norm(), u[2] / u.norm()}
	w := nrm.cross(u)
	pts := make([][2]float64, n)
	for i, v := range loop {
		p := m.vertex(v)
		pts[i] = [2]float64{p.dot(u), p.dot(w)}
	}
	ext := loopExtent(m, loop)
	eps := 1e-12 * ext * ext

	rest := make([]int, n)
	for i := range rest {
		rest[i] = i
	}
	cross := func(a, b, c int) float64 {
		return (pts[b][0]-pts[a][0])*(pts[c][1]-pts[a][1]) - (pts[b][1]-pts[a][1])*(pts[c][0]-pts[a][0])
	}
	var out []uint32
	for len(rest) > 3 {
		found := false
		for k := 0; k < len(rest) && !found; k++ {
			a, b, c := rest[(k+len(rest)-1)%len(rest)], rest[k], rest[(k+1)%len(rest)]
			if cross(a, b, c) <= eps || exists(loop[a], loop[c]) {
				continue
			}
			inside := false
			for _, o := range rest {
				if o == a || o == b || o == c {
					continue
				}
				if cross(a, b, o) >= -eps && cross(b, c, o) >= -eps && cross(c, a, o) >= -eps {
					inside = true
					break
				}
			}
			if inside {
				continue
			}
			out = append(out, loop[a], loop[b], loop[c])
			exists = withEdge(exists, loop[a], loop[c])
			rest = append(rest[:k], rest[k+1:]...)
			found = true
		}
		if !found {
			return nil
		}
	}
	return append(out, loop[rest[0]], loop[rest[1]], loop[rest[2]])
}

func withEdge(prev func(a, b uint32) bool, x, y uint32) func(a, b uint32) bool {
	return func(a, b uint32) bool { return prev(a, b) || (a == x && b == y) || (a == y && b == x) }
}
