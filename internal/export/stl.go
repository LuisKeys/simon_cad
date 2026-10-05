// Package export holds file-format helpers that are independent of the kernel.
package export

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// STLReport summarises a binary STL mesh.
type STLReport struct {
	Triangles    int
	Degenerate   int // zero-area triangles (e.g. sphere poles at fillet corners); ignored, as slicers do
	OpenEdges    int // edges used by exactly one triangle
	NonManifold  int // edges used by more than two triangles
	SignedVolume float64
}

// Watertight reports whether every edge is shared by exactly two triangles.
func (r STLReport) Watertight() bool {
	return r.Triangles > 0 && r.OpenEdges == 0 && r.NonManifold == 0
}

type vkey [3]int64

func quant(v float32) int64 { return int64(math.Round(float64(v) * 1e4)) } // 0.1 µm grid

// CheckBinarySTL parses a binary STL and checks edge manifoldness and signed volume.
func CheckBinarySTL(path string) (STLReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return STLReport{}, err
	}
	if len(data) < 84 {
		return STLReport{}, fmt.Errorf("stl too short")
	}
	n := int(binary.LittleEndian.Uint32(data[80:84]))
	if len(data) != 84+n*50 {
		return STLReport{}, fmt.Errorf("not a binary STL (size %d, triangles %d)", len(data), n)
	}
	f32 := func(off int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(data[off:])) }

	type edge [2]vkey
	counts := make(map[edge]int, n*3)
	rep := STLReport{Triangles: n}
	for i := 0; i < n; i++ {
		base := 84 + i*50 + 12 // skip normal
		var v [3]vkey
		var p [3][3]float64
		for j := 0; j < 3; j++ {
			x, y, z := f32(base+j*12), f32(base+j*12+4), f32(base+j*12+8)
			v[j] = vkey{quant(x), quant(y), quant(z)}
			p[j] = [3]float64{float64(x), float64(y), float64(z)}
		}
		if v[0] == v[1] || v[1] == v[2] || v[0] == v[2] {
			rep.Degenerate++
			continue
		}
		for j := 0; j < 3; j++ {
			a, b := v[j], v[(j+1)%3]
			if less(b, a) {
				a, b = b, a
			}
			counts[edge{a, b}]++
		}
		// signed volume of the tetrahedron against the origin
		rep.SignedVolume += (p[0][0]*(p[1][1]*p[2][2]-p[1][2]*p[2][1]) -
			p[0][1]*(p[1][0]*p[2][2]-p[1][2]*p[2][0]) +
			p[0][2]*(p[1][0]*p[2][1]-p[1][1]*p[2][0])) / 6
	}
	for _, c := range counts {
		switch {
		case c == 1:
			rep.OpenEdges++
		case c > 2:
			rep.NonManifold++
		}
	}
	return rep, nil
}

func less(a, b vkey) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
