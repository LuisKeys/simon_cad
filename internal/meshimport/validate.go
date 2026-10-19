package meshimport

import (
	"fmt"
	"math"
)

// Report summarises a mesh that was accepted.
type Report struct {
	Format    string      `json:"format"`              // "binary" | "ascii" | "3mf" | "obj"
	FileUnit  string      `json:"file_unit,omitempty"` // 3MF: the unit it declared (coordinates are already mm)
	Triangles int         `json:"triangles"`
	Vertices  int         `json:"vertices"`
	Min       [3]float64  `json:"min"`
	Max       [3]float64  `json:"max"`
	Volume    float64     `json:"volume"` // in file units³
	Flipped   bool        `json:"flipped,omitempty"`
	Filled    *FillReport `json:"filled,omitempty"`  // set when holes were closed
	Reduced   *Reduction  `json:"reduced,omitempty"` // set when the mesh was decimated
	Warnings  []string    `json:"warnings,omitempty"`
}

// Size is the bounding box extent.
func (r Report) Size() [3]float64 {
	return [3]float64{r.Max[0] - r.Min[0], r.Max[1] - r.Min[1], r.Max[2] - r.Min[2]}
}

// minEdge is the shortest edge the kernel can sew reliably (OCCT's confusion tolerance is 1e-7).
const minEdge = 1e-6

// maxProblems caps the list so a hopeless mesh gets a readable message.
const maxProblems = 8

type vec [3]float64

func (a vec) sub(b vec) vec     { return vec{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func (a vec) dot(b vec) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func (a vec) cross(b vec) vec {
	return vec{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func (a vec) norm() float64 { return math.Sqrt(a.dot(a)) }
func (m *Mesh) vertex(i uint32) vec {
	return vec{float64(m.Positions[3*i]), float64(m.Positions[3*i+1]), float64(m.Positions[3*i+2])}
}

func (m *Mesh) tri(t int) [3]vec {
	return [3]vec{m.vertex(m.Indices[3*t]), m.vertex(m.Indices[3*t+1]), m.vertex(m.Indices[3*t+2])}
}

func at(p vec) string { return fmt.Sprintf("(%.4g, %.4g, %.4g)", p[0], p[1], p[2]) }

// Validate decides whether m is one closed, consistently oriented, manifold,
// non-self-intersecting solid with a positive volume. A mesh that is entirely inside-out is
// repaired (every triangle flipped) and reported in Report.Flipped; everything else wrong is
// an *Error listing what was found and where.
func Validate(m *Mesh) (Report, error) {
	r := Report{Triangles: m.Triangles(), Vertices: len(m.Positions) / 3}
	if r.Triangles < 4 {
		return r, reject("a solid needs at least 4 triangles, the file has %d", r.Triangles)
	}
	for k := 0; k < 3; k++ {
		r.Min[k], r.Max[k] = math.Inf(1), math.Inf(-1)
	}
	for i := 0; i < r.Vertices; i++ {
		p := m.vertex(uint32(i))
		for k := 0; k < 3; k++ {
			r.Min[k], r.Max[k] = math.Min(r.Min[k], p[k]), math.Max(r.Max[k], p[k])
		}
	}
	size := r.Size()
	diag := vec{size[0], size[1], size[2]}.norm()
	if !(diag > 1e-3) {
		return r, reject("the model is a point: its bounding box is %.3g across", diag)
	}

	var probs []string
	add := func(format string, a ...any) {
		if len(probs) < maxProblems {
			probs = append(probs, fmt.Sprintf(format, a...))
		}
	}

	// Degenerate triangles: a repeated vertex after welding, or no area.
	areaTol := 1e-9 * diag * diag
	degenerate, firstDeg := 0, -1
	for t := 0; t < r.Triangles; t++ {
		i := m.Indices[3*t : 3*t+3]
		bad := i[0] == i[1] || i[1] == i[2] || i[0] == i[2]
		if !bad {
			p := m.tri(t)
			bad = p[1].sub(p[0]).cross(p[2].sub(p[0])).norm() < areaTol
		}
		if bad {
			degenerate++
			if firstDeg < 0 {
				firstDeg = t
			}
		}
	}
	if degenerate > 0 {
		add("%d degenerate triangles (zero area), the first one near %s", degenerate, at(m.tri(firstDeg)[0]))
	}

	// Tiny edges the kernel cannot sew.
	tiny, firstTiny := 0, vec{}
	for t := 0; t < r.Triangles; t++ {
		p := m.tri(t)
		for j := 0; j < 3; j++ {
			if l := p[(j+1)%3].sub(p[j]).norm(); l > 0 && l < minEdge {
				if tiny == 0 {
					firstTiny = p[j]
				}
				tiny++
			}
		}
	}
	if tiny > 0 {
		add("%d edges shorter than %g units, the first near %s: the mesh has duplicated or near-duplicated vertices", tiny, minEdge, at(firstTiny))
	}

	// Edge topology: in a closed, consistently oriented surface every edge is walked exactly
	// once in each direction.
	type edge = [2]uint32
	dir := make(map[edge]int, r.Triangles*3)
	for t := 0; t < r.Triangles; t++ {
		for j := 0; j < 3; j++ {
			a, b := m.Indices[3*t+j], m.Indices[3*t+(j+1)%3]
			if a != b {
				dir[edge{a, b}]++
			}
		}
	}
	var open, flipped, nonManifold int
	var firstOpen, firstFlip, firstNM edge
	for e, f := range dir {
		back := dir[edge{e[1], e[0]}]
		if back > 0 && e[0] > e[1] {
			continue // the undirected edge is judged once
		}
		switch {
		case f == 1 && back == 1:
		case f+back == 1:
			if open == 0 {
				firstOpen = e
			}
			open++
		case f+back == 2: // walked twice the same way: a flipped neighbour or a duplicated triangle
			if flipped == 0 {
				firstFlip = e
			}
			flipped++
		default:
			if nonManifold == 0 {
				firstNM = e
			}
			nonManifold++
		}
	}
	if open > 0 {
		add("not watertight: %d open edges (holes), the first near %s", open, at(m.vertex(firstOpen[0])))
	}
	if nonManifold > 0 {
		add("non-manifold geometry: %d edges shared by more than two triangles, the first near %s", nonManifold, at(m.vertex(firstNM[0])))
	}
	if flipped > 0 {
		add("inconsistent triangle orientation: %d edges where neighbouring triangles disagree on inside/outside (or duplicated triangles), the first near %s", flipped, at(m.vertex(firstFlip[0])))
	}
	if len(probs) > 0 {
		return r, &Error{Problems: probs, HolesOnly: open > 0 && degenerate == 0 && tiny == 0 && nonManifold == 0 && flipped == 0}
	}

	// One shell, and no vertex where two parts merely touch.
	if n := components(m); n > 1 {
		add("the file holds %d separate shells; import one body at a time", n)
	}
	if v, ok := pinched(m, dir); !ok {
		add("non-manifold vertex near %s: two parts of the surface touch at a single point", at(m.vertex(v)))
	}
	if len(probs) > 0 {
		return r, &Error{Problems: probs}
	}

	// Volume and global orientation.
	r.Volume = Volume(m)
	if math.Abs(r.Volume) < 1e-9*diag*diag*diag {
		return r, reject("the model encloses no volume (it is flat, or its parts cancel out)")
	}
	if r.Volume < 0 {
		for t := 0; t < r.Triangles; t++ {
			m.Indices[3*t+1], m.Indices[3*t+2] = m.Indices[3*t+2], m.Indices[3*t+1]
		}
		r.Volume, r.Flipped = -r.Volume, true
		r.Warnings = append(r.Warnings, "the mesh was inside-out: every triangle was flipped")
	}

	// Self-intersections: two non-adjacent triangles crossing or touching.
	if n, a, b := selfIntersections(m, diag); n > 0 {
		add("the surface intersects itself (%d crossing triangle pairs), the first near %s and %s", n, at(m.tri(a)[0]), at(m.tri(b)[0]))
		return r, &Error{Problems: probs}
	}
	return r, nil
}

// components counts connected components over shared vertices.
func components(m *Mesh) int {
	n := len(m.Positions) / 3
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	used := make([]bool, n)
	for t := 0; t < m.Triangles(); t++ {
		a, b, c := int(m.Indices[3*t]), int(m.Indices[3*t+1]), int(m.Indices[3*t+2])
		used[a], used[b], used[c] = true, true, true
		parent[find(a)] = find(b)
		parent[find(b)] = find(c)
	}
	roots := map[int]bool{}
	for v := 0; v < n; v++ {
		if used[v] {
			roots[find(v)] = true
		}
	}
	return len(roots)
}

// pinched reports a vertex whose incident triangles do not form a single fan, which makes the
// surface non-manifold there even though every edge is fine. Requires every edge to be shared by
// exactly two triangles.
func pinched(m *Mesh, dir map[[2]uint32]int) (uint32, bool) {
	nv := len(m.Positions) / 3
	owner := make(map[[2]uint32]int, len(dir))
	for t := 0; t < m.Triangles(); t++ {
		for j := 0; j < 3; j++ {
			owner[[2]uint32{m.Indices[3*t+j], m.Indices[3*t+(j+1)%3]}] = t
		}
	}
	incident := make([][]int32, nv)
	for t := 0; t < m.Triangles(); t++ {
		for j := 0; j < 3; j++ {
			v := m.Indices[3*t+j]
			incident[v] = append(incident[v], int32(t))
		}
	}
	for v := 0; v < nv; v++ {
		tris := incident[v]
		if len(tris) == 0 {
			continue
		}
		seen := map[int]bool{int(tris[0]): true}
		stack := []int{int(tris[0])}
		for len(stack) > 0 {
			t := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for j := 0; j < 3; j++ {
				a, b := m.Indices[3*t+j], m.Indices[3*t+(j+1)%3]
				if a != uint32(v) && b != uint32(v) {
					continue
				}
				if nb, ok := owner[[2]uint32{b, a}]; ok && !seen[nb] {
					seen[nb] = true
					stack = append(stack, nb)
				}
			}
		}
		if len(seen) != len(tris) {
			return uint32(v), false
		}
	}
	return 0, true
}

// Volume is the signed volume enclosed by m (positive when the triangles wind counter-clockwise
// seen from outside), computed about the bounding box centre to keep the sums small.
func Volume(m *Mesh) float64 {
	var lo, hi vec
	for k := 0; k < 3; k++ {
		lo[k], hi[k] = math.Inf(1), math.Inf(-1)
	}
	for i := 0; i < len(m.Positions)/3; i++ {
		p := m.vertex(uint32(i))
		for k := 0; k < 3; k++ {
			lo[k], hi[k] = math.Min(lo[k], p[k]), math.Max(hi[k], p[k])
		}
	}
	c := vec{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	var v float64
	for t := 0; t < m.Triangles(); t++ {
		p := m.tri(t)
		v += p[0].sub(c).dot(p[1].sub(c).cross(p[2].sub(c))) / 6
	}
	return v
}

// Bounds is the axis-aligned bounding box of m.
func Bounds(m *Mesh) (lo, hi [3]float64) {
	for k := 0; k < 3; k++ {
		lo[k], hi[k] = math.Inf(1), math.Inf(-1)
	}
	for i := 0; i < len(m.Positions)/3; i++ {
		p := m.vertex(uint32(i))
		for k := 0; k < 3; k++ {
			lo[k], hi[k] = math.Min(lo[k], p[k]), math.Max(hi[k], p[k])
		}
	}
	return lo, hi
}
