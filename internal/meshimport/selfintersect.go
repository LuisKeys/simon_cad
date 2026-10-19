package meshimport

import "math"

// selfIntersections counts pairs of triangles that cross or touch although they share no
// vertex, using a uniform grid as broad phase. It returns the count and the first pair. Touching
// counts: two parts of a surface that meet without sharing vertices cannot be sewn into a valid
// solid either.
func selfIntersections(m *Mesh, diag float64) (count, first, second int) {
	nt := m.Triangles()
	tol := 1e-7 * diag
	type box struct{ lo, hi vec }
	boxes := make([]box, nt)
	tris := make([][3]vec, nt)
	var lo, hi vec
	for k := 0; k < 3; k++ {
		lo[k], hi[k] = math.Inf(1), math.Inf(-1)
	}
	for t := 0; t < nt; t++ {
		p := m.tri(t)
		tris[t] = p
		b := box{p[0], p[0]}
		for _, v := range p[1:] {
			for k := 0; k < 3; k++ {
				b.lo[k], b.hi[k] = math.Min(b.lo[k], v[k]), math.Max(b.hi[k], v[k])
			}
		}
		boxes[t] = b
		for k := 0; k < 3; k++ {
			lo[k], hi[k] = math.Min(lo[k], b.lo[k]), math.Max(hi[k], b.hi[k])
		}
	}
	res := int(math.Cbrt(float64(nt)) * 1.5)
	if res < 1 {
		res = 1
	}
	if res > 80 {
		res = 80
	}
	var cell vec
	for k := 0; k < 3; k++ {
		cell[k] = (hi[k] - lo[k]) / float64(res)
		if cell[k] <= 0 {
			cell[k] = 1
		}
	}
	idx := func(v float64, k int) int {
		i := int((v - lo[k]) / cell[k])
		if i < 0 {
			i = 0
		}
		if i >= res {
			i = res - 1
		}
		return i
	}
	grid := make(map[int][]int32, nt)
	cellID := func(x, y, z int) int { return (x*res+y)*res + z }
	for t := 0; t < nt; t++ {
		b := boxes[t]
		for x := idx(b.lo[0]-tol, 0); x <= idx(b.hi[0]+tol, 0); x++ {
			for y := idx(b.lo[1]-tol, 1); y <= idx(b.hi[1]+tol, 1); y++ {
				for z := idx(b.lo[2]-tol, 2); z <= idx(b.hi[2]+tol, 2); z++ {
					grid[cellID(x, y, z)] = append(grid[cellID(x, y, z)], int32(t))
				}
			}
		}
	}
	shares := func(a, b int) bool {
		for i := 0; i < 3; i++ {
			for j := 0; j < 3; j++ {
				if m.Indices[3*a+i] == m.Indices[3*b+j] {
					return true
				}
			}
		}
		return false
	}
	for id, list := range grid {
		x, y, z := id/(res*res), (id/res)%res, id%res
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				a, b := int(list[i]), int(list[j])
				ba, bb := boxes[a], boxes[b]
				// Test the pair only in the cell holding the corner of the boxes' overlap.
				ok := true
				for k, c := range [3]int{x, y, z} {
					lo := math.Max(ba.lo[k], bb.lo[k]) - tol
					hi := math.Min(ba.hi[k], bb.hi[k]) + tol
					if lo > hi {
						ok = false
						break
					}
					if idx(lo, k) != c && !(c == 0 && idx(lo, k) < 0) {
						ok = false
						break
					}
				}
				if !ok || shares(a, b) {
					continue
				}
				if trianglesTouch(tris[a], tris[b], tol) {
					if count == 0 {
						first, second = a, b
					}
					count++
				}
			}
		}
	}
	return count, first, second
}

// trianglesTouch is Möller's triangle-triangle test with distances within tol treated as zero.
func trianglesTouch(a, b [3]vec, tol float64) bool {
	n2 := b[1].sub(b[0]).cross(b[2].sub(b[0]))
	l2 := n2.norm()
	n1 := a[1].sub(a[0]).cross(a[2].sub(a[0]))
	l1 := n1.norm()
	if l1 == 0 || l2 == 0 {
		return false
	}
	var da, db [3]float64
	for i := 0; i < 3; i++ {
		da[i] = snap(n2.dot(a[i].sub(b[0]))/l2, tol)
		db[i] = snap(n1.dot(b[i].sub(a[0]))/l1, tol)
	}
	if (da[0] > 0 && da[1] > 0 && da[2] > 0) || (da[0] < 0 && da[1] < 0 && da[2] < 0) {
		return false
	}
	if (db[0] > 0 && db[1] > 0 && db[2] > 0) || (db[0] < 0 && db[1] < 0 && db[2] < 0) {
		return false
	}
	if da == [3]float64{} {
		return coplanarTouch(a, b, n1, tol)
	}
	d := n1.cross(n2)
	axis := 0
	for k := 1; k < 3; k++ {
		if math.Abs(d[k]) > math.Abs(d[axis]) {
			axis = k
		}
	}
	a0, a1 := interval(a, da, axis)
	b0, b1 := interval(b, db, axis)
	if a0 > a1 {
		a0, a1 = a1, a0
	}
	if b0 > b1 {
		b0, b1 = b1, b0
	}
	return math.Max(a0, b0) <= math.Min(a1, b1)+tol
}

func snap(d, tol float64) float64 {
	if math.Abs(d) <= tol {
		return 0
	}
	return d
}

// interval is where the triangle's boundary crosses the other one's plane, projected on axis.
func interval(p [3]vec, d [3]float64, axis int) (float64, float64) {
	lone := func(i, j, k int) (float64, float64) {
		t := func(o int) float64 {
			return p[i][axis] + (p[o][axis]-p[i][axis])*d[i]/(d[i]-d[o])
		}
		return t(j), t(k)
	}
	switch {
	case d[0]*d[1] > 0:
		return lone(2, 0, 1)
	case d[0]*d[2] > 0:
		return lone(1, 0, 2)
	case d[1]*d[2] > 0 || d[0] != 0:
		return lone(0, 1, 2)
	case d[1] != 0:
		return lone(1, 0, 2)
	default:
		return lone(2, 0, 1)
	}
}

// coplanarTouch tests two triangles lying in one plane, in the 2D projection that keeps the
// most area: any pair of edges meeting, or one triangle inside the other.
func coplanarTouch(a, b [3]vec, n vec, tol float64) bool {
	i0, i1 := 1, 2
	if math.Abs(n[1]) > math.Abs(n[0]) && math.Abs(n[1]) >= math.Abs(n[2]) {
		i0, i1 = 0, 2
	} else if math.Abs(n[2]) > math.Abs(n[0]) && math.Abs(n[2]) > math.Abs(n[1]) {
		i0, i1 = 0, 1
	}
	pa, pb := proj(a, i0, i1), proj(b, i0, i1)
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if segsMeet(pa[i], pa[(i+1)%3], pb[j], pb[(j+1)%3], tol) {
				return true
			}
		}
	}
	return inTri(pa[0], pb, tol) || inTri(pb[0], pa, tol)
}

type pt [2]float64

func proj(t [3]vec, i0, i1 int) [3]pt {
	return [3]pt{{t[0][i0], t[0][i1]}, {t[1][i0], t[1][i1]}, {t[2][i0], t[2][i1]}}
}

func orient(a, b, c pt) float64 { return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0]) }

func segsMeet(a, b, c, d pt, tol float64) bool {
	o1, o2 := orient(a, b, c), orient(a, b, d)
	o3, o4 := orient(c, d, a), orient(c, d, b)
	if ((o1 > 0 && o2 < 0) || (o1 < 0 && o2 > 0)) && ((o3 > 0 && o4 < 0) || (o3 < 0 && o4 > 0)) {
		return true
	}
	onSeg := func(p, q, r pt) bool { // r on segment p-q, given collinear
		return math.Min(p[0], q[0])-tol <= r[0] && r[0] <= math.Max(p[0], q[0])+tol &&
			math.Min(p[1], q[1])-tol <= r[1] && r[1] <= math.Max(p[1], q[1])+tol
	}
	near := func(o float64, p, q pt) bool { // distance of the third point to line p-q
		l := math.Hypot(q[0]-p[0], q[1]-p[1])
		return l > 0 && math.Abs(o)/l <= tol
	}
	return (near(o1, a, b) && onSeg(a, b, c)) || (near(o2, a, b) && onSeg(a, b, d)) ||
		(near(o3, c, d) && onSeg(c, d, a)) || (near(o4, c, d) && onSeg(c, d, b))
}

func inTri(p pt, t [3]pt, tol float64) bool {
	s1, s2, s3 := orient(t[0], t[1], p), orient(t[1], t[2], p), orient(t[2], t[0], p)
	neg := s1 < -tol*tol || s2 < -tol*tol || s3 < -tol*tol
	pos := s1 > tol*tol || s2 > tol*tol || s3 > tol*tol
	return !(neg && pos)
}
