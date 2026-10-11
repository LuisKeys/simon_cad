package vectorize

import (
	"context"
	"math"
	"sort"
)

// Pt is a 2D point. Image coordinates (x right, y down, pixel units) until the result is scaled.
type Pt = [2]float64

// Loop is a closed polyline.
type Loop struct {
	Pts  []Pt
	Hole bool
}

// field is the blurred mask the contours are traced on: sub-pixel accurate, no staircase.
type field struct {
	w, h int
	v    []float32 // (w+2*pad) x (h+2*pad), padded with background
	pad  int
}

const fieldPad = 2

func blurMask(ctx context.Context, m *Mask, sigma float64) (*field, error) {
	pad := fieldPad
	W, H := m.W+2*pad, m.H+2*pad
	f := &field{w: W, h: H, v: make([]float32, W*H), pad: pad}
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			if m.Pix[y*m.W+x] {
				f.v[(y+pad)*W+x+pad] = 1
			}
		}
	}
	if sigma <= 0 {
		return f, nil
	}
	// separable gaussian, radius 2 (enough for sigma <= 1)
	r := 2
	k := make([]float32, 2*r+1)
	var sum float32
	for i := -r; i <= r; i++ {
		k[i+r] = float32(math.Exp(-float64(i*i) / (2 * sigma * sigma)))
		sum += k[i+r]
	}
	for i := range k {
		k[i] /= sum
	}
	tmp := make([]float32, W*H)
	for y := 0; y < H; y++ {
		if y&63 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for x := 0; x < W; x++ {
			var a float32
			for i := -r; i <= r; i++ {
				xx := x + i
				if xx >= 0 && xx < W {
					a += f.v[y*W+xx] * k[i+r]
				}
			}
			tmp[y*W+x] = a
		}
	}
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			var a float32
			for i := -r; i <= r; i++ {
				yy := y + i
				if yy >= 0 && yy < H {
					a += tmp[yy*W+x] * k[i+r]
				}
			}
			f.v[y*W+x] = a
		}
	}
	return f, nil
}

// edge ids: a horizontal edge joins samples (x,y)-(x+1,y), a vertical one (x,y)-(x,y+1)
type edgeID struct {
	x, y int32
	vert bool
}

type crossing struct {
	p    Pt
	link [2]edgeID
	n    int
}

// traceLoops runs marching squares at level 0.5 and returns every closed contour. Hole status is
// decided afterwards by containment parity.
func traceLoops(ctx context.Context, f *field) ([]Loop, error) {
	const level = 0.5
	W, H := f.w, f.h
	cross := map[edgeID]*crossing{}
	at := func(x, y int) float64 { return float64(f.v[y*W+x]) }
	point := func(e edgeID) Pt {
		x, y := int(e.x), int(e.y)
		// sample (i, j) sits at the centre of image pixel (i-pad, j-pad)
		if !e.vert {
			a, b := at(x, y), at(x+1, y)
			t := (level - a) / (b - a)
			return Pt{float64(x-f.pad) + 0.5 + t, float64(y-f.pad) + 0.5}
		}
		a, b := at(x, y), at(x, y+1)
		t := (level - a) / (b - a)
		return Pt{float64(x-f.pad) + 0.5, float64(y-f.pad) + 0.5 + t}
	}
	add := func(a, b edgeID) {
		for _, e := range [2]edgeID{a, b} {
			c, ok := cross[e]
			if !ok {
				c = &crossing{p: point(e)}
				cross[e] = c
			}
			o := b
			if e == b {
				o = a
			}
			if c.n < 2 {
				c.link[c.n] = o
			}
			c.n++
		}
	}
	for y := 0; y+1 < H; y++ {
		if y&63 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for x := 0; x+1 < W; x++ {
			tl, tr, br, bl := at(x, y), at(x+1, y), at(x+1, y+1), at(x, y+1)
			idx := 0
			if tl >= level {
				idx |= 1
			}
			if tr >= level {
				idx |= 2
			}
			if br >= level {
				idx |= 4
			}
			if bl >= level {
				idx |= 8
			}
			if idx == 0 || idx == 15 {
				continue
			}
			T := edgeID{int32(x), int32(y), false}
			B := edgeID{int32(x), int32(y + 1), false}
			L := edgeID{int32(x), int32(y), true}
			R := edgeID{int32(x + 1), int32(y), true}
			switch idx {
			case 1, 14:
				add(L, T)
			case 2, 13:
				add(T, R)
			case 3, 12:
				add(L, R)
			case 4, 11:
				add(R, B)
			case 6, 9:
				add(T, B)
			case 7, 8:
				add(L, B)
			case 5: // tl and br inside
				if (tl+tr+br+bl)/4 >= level {
					add(T, R)
					add(L, B)
				} else {
					add(L, T)
					add(R, B)
				}
			case 10: // tr and bl inside
				if (tl+tr+br+bl)/4 >= level {
					add(L, T)
					add(R, B)
				} else {
					add(T, R)
					add(L, B)
				}
			}
		}
	}
	// Go randomizes map iteration: walk the crossings in raster order so a trace is repeatable
	// (same loops, same order, same start points) every time.
	keys := make([]edgeID, 0, len(cross))
	for k := range cross {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.y != b.y {
			return a.y < b.y
		}
		if a.x != b.x {
			return a.x < b.x
		}
		return !a.vert && b.vert
	})
	var loops []Loop
	visited := map[edgeID]bool{}
	for _, start := range keys {
		c := cross[start]
		if visited[start] || c.n != 2 {
			continue
		}
		var pts []Pt
		prev, cur := edgeID{-1, -1, false}, start
		for {
			visited[cur] = true
			cc := cross[cur]
			pts = append(pts, cc.p)
			next := cc.link[0]
			if next == prev {
				next = cc.link[1]
			}
			if cc.n != 2 {
				pts = nil
				break
			}
			prev, cur = cur, next
			if cur == start {
				break
			}
			if visited[cur] { // closed onto an earlier part: not a clean loop
				pts = nil
				break
			}
		}
		if len(pts) >= 3 {
			loops = append(loops, Loop{Pts: pts})
		}
	}
	return loops, nil
}

func signedArea(p []Pt) float64 {
	a := 0.0
	for i := range p {
		j := (i + 1) % len(p)
		a += p[i][0]*p[j][1] - p[j][0]*p[i][1]
	}
	return a / 2
}

func pointInPoly(pt Pt, poly []Pt) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		if (poly[i][1] > pt[1]) != (poly[j][1] > pt[1]) &&
			pt[0] < (poly[j][0]-poly[i][0])*(pt[1]-poly[i][1])/(poly[j][1]-poly[i][1])+poly[i][0] {
			in = !in
		}
	}
	return in
}

type bbox struct{ x0, y0, x1, y1 float64 }

func boxOf(p []Pt) bbox {
	b := bbox{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, q := range p {
		b.x0, b.y0 = math.Min(b.x0, q[0]), math.Min(b.y0, q[1])
		b.x1, b.y1 = math.Max(b.x1, q[0]), math.Max(b.y1, q[1])
	}
	return b
}

func (b bbox) contains(o bbox) bool {
	return b.x0 <= o.x0 && b.y0 <= o.y0 && b.x1 >= o.x1 && b.y1 >= o.y1
}

// Nest finds each loop's parent (the smallest loop containing it) and marks holes: a loop is a
// hole when it sits inside an odd number of others. Returns parent indices (-1 for none).
func Nest(loops []Loop) []int {
	n := len(loops)
	boxes := make([]bbox, n)
	areas := make([]float64, n)
	for i := range loops {
		boxes[i] = boxOf(loops[i].Pts)
		areas[i] = math.Abs(signedArea(loops[i].Pts))
	}
	parent := make([]int, n)
	depth := make([]int, n)
	for i := range loops {
		parent[i] = -1
		probe := loops[i].Pts[0]
		best := -1
		for j := range loops {
			if i == j || !boxes[j].contains(boxes[i]) || areas[j] <= areas[i] {
				continue
			}
			if pointInPoly(probe, loops[j].Pts) {
				depth[i]++
				if best < 0 || areas[j] < areas[best] {
					best = j
				}
			}
		}
		parent[i] = best
	}
	for i := range loops {
		loops[i].Hole = depth[i]%2 == 1
		// a hole's parent is the foreground loop around it; an outer inside a hole has the hole as parent
	}
	return parent
}
