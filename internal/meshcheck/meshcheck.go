// Package meshcheck validates an in-memory triangle mesh the way a slicer would see it:
// watertightness, non-manifold edges, degenerate triangles, connected components, volume.
package meshcheck

import "math"

// Report summarises a mesh.
type Report struct {
	Triangles    int
	Degenerate   int // zero-area triangles; ignored in the other counts
	OpenEdges    int // edges used by exactly one triangle
	NonManifold  int // edges used by more than two triangles
	Components   int // connected components over shared vertices
	SignedVolume float64
	// MinX.. is the axis-aligned bounding box.
	Min, Max [3]float64
}

// Watertight reports whether every edge is shared by exactly two triangles.
func (r Report) Watertight() bool {
	return r.Triangles > 0 && r.OpenEdges == 0 && r.NonManifold == 0
}

type vkey [3]int64

// grid is the vertex-merge resolution (0.1 µm): face seams in a tessellation share exact nodes,
// the grid only absorbs float32 rounding.
const grid = 1e4

func quant(v float32) int64 { return int64(math.Round(float64(v) * grid)) }

// Check analyses positions (xyz triples) and indices (triangle triples).
func Check(positions []float32, indices []uint32) Report {
	r := Report{Triangles: len(indices) / 3}
	for k := 0; k < 3; k++ {
		r.Min[k], r.Max[k] = math.Inf(1), math.Inf(-1)
	}
	ids := map[vkey]int{}
	vid := func(i uint32) int {
		p := positions[3*i : 3*i+3]
		k := vkey{quant(p[0]), quant(p[1]), quant(p[2])}
		id, ok := ids[k]
		if !ok {
			id = len(ids)
			ids[k] = id
		}
		return id
	}
	type edge [2]int
	counts := make(map[edge]int, len(indices))
	var parent []int
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for t := 0; t+2 < len(indices); t += 3 {
		var v [3]int
		var p [3][3]float64
		for j := 0; j < 3; j++ {
			i := indices[t+j]
			v[j] = vid(i)
			for k := 0; k < 3; k++ {
				p[j][k] = float64(positions[3*i+uint32(k)])
				r.Min[k], r.Max[k] = math.Min(r.Min[k], p[j][k]), math.Max(r.Max[k], p[j][k])
			}
		}
		for len(parent) < len(ids) {
			parent = append(parent, len(parent))
		}
		if v[0] == v[1] || v[1] == v[2] || v[0] == v[2] {
			r.Degenerate++
			continue
		}
		for j := 0; j < 3; j++ {
			a, b := v[j], v[(j+1)%3]
			if b < a {
				a, b = b, a
			}
			counts[edge{a, b}]++
			parent[find(a)] = find(b)
		}
		r.SignedVolume += (p[0][0]*(p[1][1]*p[2][2]-p[1][2]*p[2][1]) -
			p[0][1]*(p[1][0]*p[2][2]-p[1][2]*p[2][0]) +
			p[0][2]*(p[1][0]*p[2][1]-p[1][1]*p[2][0])) / 6
	}
	for _, c := range counts {
		switch {
		case c == 1:
			r.OpenEdges++
		case c > 2:
			r.NonManifold++
		}
	}
	roots := map[int]bool{}
	used := map[int]bool{}
	for e := range counts {
		used[e[0]], used[e[1]] = true, true
	}
	for v := range used {
		roots[find(v)] = true
	}
	r.Components = len(roots)
	return r
}
