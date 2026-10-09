// Package testmesh builds small watertight binary STL meshes for tests.
package testmesh

import (
	"encoding/binary"
	"math"
	"os"
)

// Tri is one triangle, counter-clockwise seen from outside.
type Tri [3][3]float32

// WriteSTL writes triangles as a binary STL.
func WriteSTL(path string, tris []Tri) error {
	b := make([]byte, 84+50*len(tris))
	binary.LittleEndian.PutUint32(b[80:], uint32(len(tris)))
	for i, t := range tris {
		o := 84 + 50*i + 12
		for _, v := range t {
			for _, c := range v {
				binary.LittleEndian.PutUint32(b[o:], math.Float32bits(c))
				o += 4
			}
		}
	}
	return os.WriteFile(path, b, 0o600)
}

// Prism extrudes a polygon with optional hole between z0 and z1. outer must be
// counter-clockwise; hole (if any) is given counter-clockwise too and is reversed here.
// outer and hole must have the same number of points, ordered so that point i of the hole
// faces point i of the outer ring (true for concentric circles), because the end caps are
// built as a quad strip.
func Prism(outer, hole [][2]float32, z0, z1 float32) []Tri {
	var out []Tri
	p3 := func(p [2]float32, z float32) [3]float32 { return [3]float32{p[0], p[1], z} }
	side := func(ring [][2]float32) {
		for i, a := range ring {
			b := ring[(i+1)%len(ring)]
			out = append(out, Tri{p3(a, z0), p3(b, z0), p3(b, z1)}, Tri{p3(a, z0), p3(b, z1), p3(a, z1)})
		}
	}
	side(outer)
	if hole == nil {
		var cx, cy float32
		for _, p := range outer {
			cx += p[0]
			cy += p[1]
		}
		c := [2]float32{cx / float32(len(outer)), cy / float32(len(outer))}
		for i, a := range outer {
			b := outer[(i+1)%len(outer)]
			out = append(out, Tri{p3(c, z1), p3(a, z1), p3(b, z1)}, Tri{p3(c, z0), p3(b, z0), p3(a, z0)})
		}
		return out
	}
	rev := make([][2]float32, len(hole))
	for i, p := range hole {
		rev[len(hole)-1-i] = p
	}
	side(rev)
	for i := range outer {
		j := (i + 1) % len(outer)
		o0, o1, i0, i1 := outer[i], outer[j], hole[i], hole[j]
		out = append(out,
			Tri{p3(o0, z1), p3(o1, z1), p3(i1, z1)}, Tri{p3(o0, z1), p3(i1, z1), p3(i0, z1)},
			Tri{p3(o0, z0), p3(i1, z0), p3(o1, z0)}, Tri{p3(o0, z0), p3(i0, z0), p3(i1, z0)})
	}
	return out
}

// Rect returns a counter-clockwise rectangle.
func Rect(x0, y0, x1, y1 float32) [][2]float32 {
	return [][2]float32{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
}

// Circle returns a counter-clockwise polygon of n points.
func Circle(cx, cy, r float32, n int) [][2]float32 {
	out := make([][2]float32, n)
	for i := range out {
		a := 2 * math.Pi * float64(i) / float64(n)
		out[i] = [2]float32{cx + r*float32(math.Cos(a)), cy + r*float32(math.Sin(a))}
	}
	return out
}

// Box is a closed axis-aligned box.
func Box(x0, y0, z0, x1, y1, z1 float32) []Tri { return Prism(Rect(x0, y0, x1, y1), nil, z0, z1) }
