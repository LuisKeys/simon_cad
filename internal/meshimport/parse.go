// Package meshimport reads and validates triangle meshes (STL) before they are allowed into a
// project. A mesh is only accepted when it describes one closed, consistently oriented,
// non-self-intersecting solid: anything weaker would make the B-Rep conversion in the kernel
// fail late, or worse, succeed with a wrong shape. Accepted meshes are stored in a canonical
// binary form, so the same model imported as ASCII or binary has the same content id.
package meshimport

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
)

const (
	// MaxBytes bounds the size of a file that is even looked at.
	MaxBytes = 64 << 20
	// DefaultMaxTriangles bounds the model size: every triangle becomes a face in the kernel.
	DefaultMaxTriangles = 100000
	// maxCoord rejects garbage floats (uninitialised memory in a binary STL looks like 1e30).
	maxCoord = 1e7
)

// Mesh is a welded triangle mesh: vertices shared by exact position, triangles as index triples.
type Mesh struct {
	Positions []float32 // xyz triples
	Indices   []uint32  // 3 per triangle
}

// Triangles is the triangle count.
func (m *Mesh) Triangles() int { return len(m.Indices) / 3 }

// Error is a rejected mesh: every problem found, in the order they were detected.
type Error struct {
	Problems []string
	// HolesOnly is set when open edges (holes) are the only thing wrong, so closing them is worth
	// offering (Options.FillHoles).
	HolesOnly bool
}

func (e *Error) Error() string {
	switch len(e.Problems) {
	case 0:
		return "invalid mesh"
	case 1:
		return e.Problems[0]
	}
	s := e.Problems[0]
	for _, p := range e.Problems[1:] {
		s += "; " + p
	}
	return s
}

func reject(format string, a ...any) error {
	return &Error{Problems: []string{fmt.Sprintf(format, a...)}}
}

// Parse reads an ASCII or binary STL into a welded mesh. It checks the file's structure and
// that every coordinate is a sane finite number; whether the mesh is a usable solid is what
// Validate decides.
func Parse(data []byte, maxTris int) (m *Mesh, format string, err error) {
	if maxTris <= 0 {
		maxTris = DefaultMaxTriangles
	}
	if len(data) > MaxBytes {
		return nil, "", reject("file is %d MB, the limit is %d MB", len(data)>>20, MaxBytes>>20)
	}
	if len(data) == 0 {
		return nil, "", reject("file is empty")
	}
	if len(data) >= 84 {
		n := int(binary.LittleEndian.Uint32(data[80:84]))
		if len(data) == 84+n*50 {
			m, err = parseBinary(data, n, maxTris)
			return m, "binary", err
		}
	}
	if looksASCII(data) {
		m, err = parseASCII(data, maxTris)
		return m, "ascii", err
	}
	if len(data) >= 84 {
		n := int(binary.LittleEndian.Uint32(data[80:84]))
		if len(data) < 84+n*50 {
			return nil, "", reject("binary STL is truncated: header declares %d triangles (%d bytes) but the file has %d bytes", n, 84+n*50, len(data))
		}
		return nil, "", reject("binary STL has %d trailing bytes after its %d triangles", len(data)-84-n*50, n)
	}
	return nil, "", reject("not an STL file (neither a binary STL nor an ASCII one)")
}

func looksASCII(data []byte) bool {
	t := bytes.TrimLeft(data, " \t\r\n\xef\xbb\xbf")
	if len(t) < 5 || !bytes.EqualFold(t[:5], []byte("solid")) {
		return false
	}
	head := t
	if len(head) > 4096 {
		head = head[:4096]
	}
	return bytes.Contains(bytes.ToLower(head), []byte("facet")) || bytes.Contains(bytes.ToLower(head), []byte("endsolid"))
}

type welder struct {
	ids map[[3]uint32]uint32
	m   *Mesh
}

func newWelder(capTris int) *welder {
	return &welder{ids: make(map[[3]uint32]uint32, capTris/2+8), m: &Mesh{Indices: make([]uint32, 0, capTris*3)}}
}

// add welds a vertex by its exact float32 value (-0 and +0 are the same point).
func (w *welder) add(x, y, z float32) uint32 {
	key := [3]uint32{bits(x), bits(y), bits(z)}
	if id, ok := w.ids[key]; ok {
		return id
	}
	id := uint32(len(w.m.Positions) / 3)
	w.m.Positions = append(w.m.Positions, x, y, z)
	w.ids[key] = id
	return id
}

func bits(f float32) uint32 {
	if f == 0 {
		return 0
	}
	return math.Float32bits(f)
}

func okCoord(v float32) bool {
	f := float64(v)
	return !math.IsNaN(f) && !math.IsInf(f, 0) && math.Abs(f) <= maxCoord
}

func parseBinary(data []byte, n, maxTris int) (*Mesh, error) {
	if n == 0 {
		return nil, reject("binary STL declares no triangles")
	}
	if n > maxTris {
		return nil, reject("mesh has %d triangles, the limit is %d: decimate it first", n, maxTris)
	}
	w := newWelder(n)
	for i := 0; i < n; i++ {
		base := 84 + i*50 + 12
		var v [3]uint32
		for j := 0; j < 3; j++ {
			var c [3]float32
			for k := 0; k < 3; k++ {
				c[k] = math.Float32frombits(binary.LittleEndian.Uint32(data[base+12*j+4*k:]))
				if !okCoord(c[k]) {
					return nil, reject("triangle %d has a non-finite or absurd coordinate (%v)", i, c[k])
				}
			}
			v[j] = w.add(c[0], c[1], c[2])
		}
		w.m.Indices = append(w.m.Indices, v[0], v[1], v[2])
	}
	return w.m, nil
}

func parseASCII(data []byte, maxTris int) (*Mesh, error) {
	toks := bytes.Fields(data)
	p := 0
	next := func() []byte {
		if p >= len(toks) {
			return nil
		}
		t := toks[p]
		p++
		return t
	}
	is := func(t []byte, kw string) bool { return bytes.EqualFold(t, []byte(kw)) }
	num := func() (float32, error) {
		t := next()
		if t == nil {
			return 0, reject("ASCII STL ends in the middle of a facet")
		}
		f, err := strconv.ParseFloat(string(t), 32)
		if err != nil && !math.IsInf(f, 0) {
			return 0, reject("ASCII STL has %q where a number was expected", t)
		}
		if !okCoord(float32(f)) {
			return 0, reject("ASCII STL has a non-finite or absurd number (%q)", t)
		}
		return float32(f), nil
	}
	if t := next(); t == nil || !is(t, "solid") {
		return nil, reject("ASCII STL does not start with \"solid\"")
	}
	for p < len(toks) && !is(toks[p], "facet") && !is(toks[p], "endsolid") {
		p++ // the solid's name
	}
	w := newWelder(1024)
	tris := 0
	for {
		t := next()
		if t == nil {
			return nil, reject("ASCII STL has no \"endsolid\" (truncated file?)")
		}
		if is(t, "endsolid") {
			break
		}
		if !is(t, "facet") {
			return nil, reject("ASCII STL has %q where \"facet\" or \"endsolid\" was expected (facet %d)", t, tris+1)
		}
		if tris >= maxTris {
			return nil, reject("mesh has more than %d triangles, the limit: decimate it first", maxTris)
		}
		if t := next(); t == nil || !is(t, "normal") {
			return nil, reject("facet %d lacks its \"normal\"", tris+1)
		}
		for k := 0; k < 3; k++ { // the declared normal is not trusted, only skipped
			if _, err := num(); err != nil {
				return nil, err
			}
		}
		if a, b := next(), next(); a == nil || b == nil || !is(a, "outer") || !is(b, "loop") {
			return nil, reject("facet %d lacks its \"outer loop\"", tris+1)
		}
		var v [3]uint32
		for j := 0; j < 3; j++ {
			if t := next(); t == nil || !is(t, "vertex") {
				return nil, reject("facet %d does not have exactly 3 vertices", tris+1)
			}
			var c [3]float32
			for k := 0; k < 3; k++ {
				f, err := num()
				if err != nil {
					return nil, err
				}
				c[k] = f
			}
			v[j] = w.add(c[0], c[1], c[2])
		}
		if t := next(); t == nil || !is(t, "endloop") {
			return nil, reject("facet %d does not have exactly 3 vertices", tris+1)
		}
		if t := next(); t == nil || !is(t, "endfacet") {
			return nil, reject("facet %d lacks its \"endfacet\"", tris+1)
		}
		w.m.Indices = append(w.m.Indices, v[0], v[1], v[2])
		tris++
	}
	if tris == 0 {
		return nil, reject("ASCII STL contains no facets")
	}
	for p < len(toks) && !is(toks[p], "solid") {
		p++ // the name repeated after "endsolid"
	}
	if p < len(toks) {
		return nil, reject("ASCII STL has more content after \"endsolid\": several solids in one file are not supported")
	}
	return w.m, nil
}
