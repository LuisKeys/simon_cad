package builtin

import "math"

type seg struct{ a, b pt }

type key [2]int64

func keyOf(p pt) key { return key{int64(math.Round(p.x * 1e4)), int64(math.Round(p.y * 1e4))} }

// crossing is where the edge a→b meets the plane z. The endpoints are ordered canonically
// so the two triangles sharing an edge compute bit-identical points and the loops close.
func crossing(a, b [3]float64, z float64) pt {
	if a[0] > b[0] || (a[0] == b[0] && (a[1] > b[1] || (a[1] == b[1] && a[2] > b[2]))) {
		a, b = b, a
	}
	t := (z - a[2]) / (b[2] - a[2])
	return pt{a[0] + t*(b[0]-a[0]), a[1] + t*(b[1]-a[1])}
}

// sliceTriangle returns the directed segment where triangle t meets plane z, oriented with
// the material on its left (counter-clockwise outer loops).
func sliceTriangle(t []float32, z float64) (seg, bool) {
	var v [3][3]float64
	var below [3]bool
	nb := 0
	for i := 0; i < 3; i++ {
		v[i] = [3]float64{float64(t[3*i]), float64(t[3*i+1]), float64(t[3*i+2])}
		below[i] = v[i][2] < z
		if below[i] {
			nb++
		}
	}
	if nb == 0 || nb == 3 {
		return seg{}, false
	}
	lone := 0 // the vertex alone on its side
	for i := 0; i < 3; i++ {
		if below[i] == (nb == 1) {
			lone = i
		}
	}
	o1, o2 := v[(lone+1)%3], v[(lone+2)%3]
	a, b := crossing(v[lone], o1, z), crossing(v[lone], o2, z)
	e1 := [3]float64{v[1][0] - v[0][0], v[1][1] - v[0][1], v[1][2] - v[0][2]}
	e2 := [3]float64{v[2][0] - v[0][0], v[2][1] - v[0][1], v[2][2] - v[0][2]}
	nx := e1[1]*e2[2] - e1[2]*e2[1]
	ny := e1[2]*e2[0] - e1[0]*e2[2]
	d := b.sub(a)
	if d.dot(pt{-ny, nx}) < 0 {
		a, b = b, a
	}
	if dist(a, b) < 1e-9 {
		return seg{}, false
	}
	return seg{a, b}, true
}

// chain joins directed segments into closed loops. Open chains are counted and dropped.
func chain(segs []seg) (loops []poly, open int) {
	start := make(map[key][]int, len(segs))
	for i, s := range segs {
		k := keyOf(s.a)
		start[k] = append(start[k], i)
	}
	used := make([]bool, len(segs))
	for i := range segs {
		if used[i] {
			continue
		}
		var loop poly
		first := keyOf(segs[i].a)
		cur := i
		closed := false
		for {
			used[cur] = true
			loop = append(loop, segs[cur].a)
			end := keyOf(segs[cur].b)
			if end == first {
				closed = true
				break
			}
			next := -1
			for _, c := range start[end] {
				if !used[c] {
					next = c
					break
				}
			}
			if next < 0 {
				break
			}
			cur = next
		}
		if !closed {
			open++
			continue
		}
		if l := loop.clean(); len(l) >= 3 && math.Abs(l.area()) > 1e-4 {
			loops = append(loops, l)
		}
	}
	return
}

// island is an outer boundary with the holes inside it.
type island struct {
	outer poly
	holes []poly
}

// islands groups loops: positive area is an outer boundary, negative a hole, attached to
// the smallest outer boundary that contains it.
func islands(loops []poly) []island {
	var out []island
	for _, l := range loops {
		if l.area() > 0 {
			out = append(out, island{outer: l})
		}
	}
	for _, l := range loops {
		if l.area() >= 0 {
			continue
		}
		best := -1
		for i, is := range out {
			if is.outer.contains(l[0]) && (best < 0 || is.outer.area() < out[best].outer.area()) {
				best = i
			}
		}
		if best >= 0 {
			out[best].holes = append(out[best].holes, l)
		}
	}
	return out
}
