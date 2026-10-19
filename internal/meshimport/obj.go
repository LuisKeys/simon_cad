package meshimport

import (
	"bytes"
	"math"
	"strconv"
)

// OBJ is a text format with no units and no magic number. Only the geometry is read: vertices (`v`)
// and faces (`f`). Faces with more than three vertices are triangulated in their own plane (ear
// clipping), so quads and concave polygons come out right; texture coordinates, normals, groups,
// smoothing and materials are ignored. Free-form curves and surfaces are refused.

// maxOBJVertices bounds the vertex list of a file (unused vertices cost nothing later).
const maxOBJVertices = 1 << 22

// objIgnored are the statements that carry no geometry we need.
var objIgnored = map[string]bool{
	"vt": true, "vn": true, "vp": true, "mtllib": true, "usemtl": true, "o": true, "g": true, "s": true,
	"l": true, "p": true, "mg": true, "usemap": true, "bevel": true, "c_interp": true, "d_interp": true,
	"lod": true, "shadow_obj": true, "trace_obj": true, "ctech": true, "stech": true,
}

// objFreeForm are the statements of free-form geometry, which is not a triangle mesh.
var objFreeForm = map[string]bool{
	"cstype": true, "deg": true, "bmat": true, "step": true, "curv": true, "curv2": true, "surf": true,
	"parm": true, "trim": true, "hole": true, "scrv": true, "sp": true, "end": true, "con": true,
}

// looksOBJ reports whether data is plausibly a Wavefront OBJ: text whose first statement is a
// known keyword and that has at least one vertex line near the start.
func looksOBJ(data []byte) bool {
	head := data
	if len(head) > 1<<16 {
		head = head[:1<<16]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	first, hasV := true, false
	for _, line := range bytes.Split(head, []byte("\n")) {
		f := bytes.Fields(line)
		if len(f) == 0 || f[0][0] == '#' {
			continue
		}
		kw := string(f[0])
		if first {
			first = false
			if kw == "v" || objIgnored[kw] || objFreeForm[kw] || kw == "f" {
				// fine
			} else {
				return false
			}
		}
		if kw == "v" {
			hasV = true
			break
		}
	}
	return hasV
}

type objFace struct {
	line int
	idx  []int // 0-based OBJ vertex indices
}

// parseOBJ reads an OBJ into a welded mesh in file units.
func parseOBJ(data []byte, maxTris int) (*Mesh, error) {
	var verts []float32 // xyz triples, in file order
	var faces []objFace
	lines := bytes.Split(data, []byte("\n"))
	for i := 0; i < len(lines); i++ {
		ln := i + 1
		line := bytes.TrimRight(lines[i], "\r")
		for bytes.HasSuffix(line, []byte("\\")) && i+1 < len(lines) { // continued line
			i++
			line = append(bytes.TrimSuffix(line, []byte("\\")), ' ')
			line = append(line, bytes.TrimRight(lines[i], "\r")...)
		}
		if k := bytes.IndexByte(line, '#'); k >= 0 {
			line = line[:k]
		}
		f := bytes.Fields(line)
		if len(f) == 0 {
			continue
		}
		kw := string(bytes.TrimPrefix(f[0], []byte("\xef\xbb\xbf")))
		switch {
		case kw == "v":
			if n := len(f) - 1; n != 3 && n != 4 && n != 6 {
				return nil, reject("OBJ line %d: a vertex needs 3 coordinates (x y z), found %d", ln, n)
			}
			for k := 1; k <= 3; k++ {
				v, err := strconv.ParseFloat(string(f[k]), 32)
				if err != nil && !math.IsInf(v, 0) {
					return nil, reject("OBJ line %d has %q where a number was expected", ln, f[k])
				}
				if !okCoord(float32(v)) {
					return nil, reject("OBJ line %d has a non-finite or absurd coordinate (%q)", ln, f[k])
				}
				verts = append(verts, float32(v))
			}
			if len(verts)/3 > maxOBJVertices {
				return nil, reject("OBJ has more than %d vertices", maxOBJVertices)
			}
		case kw == "f":
			if len(f) < 4 {
				return nil, reject("OBJ line %d: a face needs at least 3 vertices", ln)
			}
			nv := len(verts) / 3
			fc := objFace{line: ln}
			for _, tok := range f[1:] {
				if k := bytes.IndexByte(tok, '/'); k >= 0 {
					tok = tok[:k]
				}
				n, err := strconv.Atoi(string(tok))
				if err != nil || n == 0 {
					return nil, reject("OBJ line %d has %q where a vertex index was expected", ln, tok)
				}
				if n < 0 {
					n = nv + n + 1 // relative to the vertices read so far
				}
				if n < 1 || n > nv {
					return nil, reject("OBJ line %d refers to vertex %s, but only %d vertices are defined before it", ln, tok, nv)
				}
				fc.idx = append(fc.idx, n-1)
			}
			faces = append(faces, fc)
			if len(faces) > 4*maxTris {
				return nil, reject("OBJ has more than %d faces, the limit is %d triangles: decimate it first", 4*maxTris, maxTris)
			}
		case objIgnored[kw]:
		case objFreeForm[kw]:
			return nil, reject("OBJ line %d uses free-form curves or surfaces (%q): only polygon meshes are supported", ln, kw)
		default:
			return nil, reject("OBJ line %d has the unknown statement %q", ln, kw)
		}
	}
	if len(faces) == 0 {
		return nil, reject("OBJ contains no faces")
	}
	w := newWelder(len(faces) * 2)
	ids := make([]int64, len(verts)/3) // welded id of each OBJ vertex, -1 until a face uses it
	for i := range ids {
		ids[i] = -1
	}
	weld := func(i int) uint32 {
		if ids[i] < 0 {
			ids[i] = int64(w.add(verts[3*i], verts[3*i+1], verts[3*i+2]))
		}
		return uint32(ids[i])
	}
	tris := 0
	for _, fc := range faces {
		var tri [][3]int
		if len(fc.idx) == 3 {
			tri = [][3]int{{0, 1, 2}}
		} else {
			var err error
			if tri, err = triangulatePolygon(verts, fc.idx); err != nil {
				return nil, reject("OBJ line %d: %v", fc.line, err)
			}
		}
		for _, t := range tri {
			if tris++; tris > maxTris {
				return nil, reject("mesh has more than %d triangles, the limit: decimate it first", maxTris)
			}
			w.m.Indices = append(w.m.Indices, weld(fc.idx[t[0]]), weld(fc.idx[t[1]]), weld(fc.idx[t[2]]))
		}
	}
	return w.m, nil
}

// triangulatePolygon splits a planar polygon (indices into verts) into triangles by ear clipping in
// the plane given by Newell's normal. The triangles keep the polygon's winding. A polygon with no
// area, or one that crosses itself, is refused.
func triangulatePolygon(verts []float32, idx []int) ([][3]int, error) {
	n := len(idx)
	p := func(k int) vec {
		i := idx[k]
		return vec{float64(verts[3*i]), float64(verts[3*i+1]), float64(verts[3*i+2])}
	}
	var nrm vec
	for k := 0; k < n; k++ {
		a, b := p(k), p((k+1)%n)
		nrm[0] += (a[1] - b[1]) * (a[2] + b[2])
		nrm[1] += (a[2] - b[2]) * (a[0] + b[0])
		nrm[2] += (a[0] - b[0]) * (a[1] + b[1])
	}
	nl := nrm.norm()
	if nl == 0 {
		return nil, errPolygon("has no area")
	}
	nrm = vec{nrm[0] / nl, nrm[1] / nl, nrm[2] / nl}
	rest := make([]int, n)
	for i := range rest {
		rest[i] = i
	}
	inTri := func(a, b, c, q vec) bool {
		for _, e := range [][2]vec{{a, b}, {b, c}, {c, a}} {
			if e[1].sub(e[0]).cross(q.sub(e[0])).dot(nrm) < 0 {
				return false
			}
		}
		return true
	}
	var out [][3]int
	for len(rest) > 3 {
		cut := -1
		for k := range rest {
			ia, ib, ic := rest[(k+len(rest)-1)%len(rest)], rest[k], rest[(k+1)%len(rest)]
			a, b, c := p(ia), p(ib), p(ic)
			if b.sub(a).cross(c.sub(b)).dot(nrm) <= 0 { // reflex or flat corner: not an ear
				continue
			}
			ear := true
			for _, o := range rest {
				if o == ia || o == ib || o == ic {
					continue
				}
				if q := p(o); inTri(a, b, c, q) {
					ear = false
					break
				}
			}
			if ear {
				out = append(out, [3]int{ia, ib, ic})
				cut = k
				break
			}
		}
		if cut < 0 {
			return nil, errPolygon("cannot be split into triangles (it is not planar or it crosses itself)")
		}
		rest = append(rest[:cut], rest[cut+1:]...)
	}
	return append(out, [3]int{rest[0], rest[1], rest[2]}), nil
}

type errPolygon string

func (e errPolygon) Error() string { return "a face with " + string(e) }
