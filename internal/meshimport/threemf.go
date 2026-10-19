package meshimport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// 3MF is a ZIP of XML parts. Unlike STL it states its length unit, so its coordinates are
// converted to millimetres here and the mesh enters the project already in mm (same node, same
// validation as an STL). Only what defines the solid is read: vertices, triangles, component
// trees and build transforms. Colours, materials and thumbnails are ignored; features that
// change the geometry (external model parts, support or surface objects, unknown required
// extensions) are refused instead of silently dropped.

const (
	maxPartBytes   = 4 * MaxBytes // uncompressed size of the model part: bounds a zip bomb
	maxComposeDeep = 16           // nesting of components
	maxInstances   = 4096         // object instances produced by components and build items
)

// threeMFUnits is the length of one 3MF unit, in millimetres.
var threeMFUnits = map[string]float64{
	"micron": 0.001, "millimeter": 1, "centimeter": 10, "inch": 25.4, "foot": 304.8, "meter": 1000,
}

// knownExtensions are the 3MF extension namespaces whose data does not alter the geometry.
var knownExtensions = map[string]bool{
	"http://schemas.microsoft.com/3dmanufacturing/material/2015/02":   true,
	"http://schemas.microsoft.com/3dmanufacturing/production/2015/06": true,
}

func isZip(data []byte) bool { return bytes.HasPrefix(data, []byte("PK\x03\x04")) }

type mat [12]float64 // 3MF row-vector transform: m00 m01 m02 m10 m11 m12 m20 m21 m22 m30 m31 m32

var identity = mat{1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0}

func (m mat) apply(p [3]float64) [3]float64 {
	return [3]float64{
		p[0]*m[0] + p[1]*m[3] + p[2]*m[6] + m[9],
		p[0]*m[1] + p[1]*m[4] + p[2]*m[7] + m[10],
		p[0]*m[2] + p[1]*m[5] + p[2]*m[8] + m[11],
	}
}

// then returns the transform that applies m first and n second.
func (m mat) then(n mat) mat {
	var r mat
	for i := 0; i < 4; i++ {
		row := [3]float64{m[3*i], m[3*i+1], m[3*i+2]}
		for j := 0; j < 3; j++ {
			r[3*i+j] = row[0]*n[j] + row[1]*n[3+j] + row[2]*n[6+j]
			if i == 3 {
				r[3*i+j] += n[9+j]
			}
		}
	}
	return r
}

func (m mat) det() float64 {
	return m[0]*(m[4]*m[8]-m[5]*m[7]) - m[1]*(m[3]*m[8]-m[5]*m[6]) + m[2]*(m[3]*m[7]-m[4]*m[6])
}

type tmfComponent struct {
	object int
	t      mat
}

type tmfObject struct {
	typ        string
	verts      [][3]float64
	tris       [][3]int
	hasMesh    bool
	components []tmfComponent
}

type tmfItem struct {
	object int
	t      mat
}

func parseTransform(s string) (mat, error) {
	if strings.TrimSpace(s) == "" {
		return identity, nil
	}
	f := strings.Fields(s)
	if len(f) != 12 {
		return mat{}, fmt.Errorf("transform %q does not have 12 numbers", s)
	}
	var m mat
	for i, t := range f {
		v, err := strconv.ParseFloat(t, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return mat{}, fmt.Errorf("transform %q has a bad number %q", s, t)
		}
		m[i] = v
	}
	return m, nil
}

// find returns a zip entry by OPC name (case-insensitive, with or without a leading slash).
func find(zr *zip.Reader, name string) *zip.File {
	name = strings.TrimPrefix(name, "/")
	for _, f := range zr.File {
		if strings.EqualFold(strings.TrimPrefix(f.Name, "/"), name) {
			return f
		}
	}
	return nil
}

func readPart(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxPartBytes {
		return nil, reject("3MF part %s is %d MB uncompressed, the limit is %d MB", f.Name, f.UncompressedSize64>>20, maxPartBytes>>20)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, reject("3MF part %s cannot be read: %v", f.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxPartBytes+1))
	if err != nil {
		return nil, reject("3MF part %s is damaged: %v", f.Name, err)
	}
	if len(b) > maxPartBytes {
		return nil, reject("3MF part %s is larger than %d MB uncompressed", f.Name, maxPartBytes>>20)
	}
	return b, nil
}

// modelPart locates the root model part through the package relationships.
func modelPart(zr *zip.Reader) (*zip.File, error) {
	if rels := find(zr, "_rels/.rels"); rels != nil {
		b, err := readPart(rels)
		if err != nil {
			return nil, err
		}
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
				var typ, target string
				for _, a := range se.Attr {
					switch a.Name.Local {
					case "Type":
						typ = a.Value
					case "Target":
						target = a.Value
					}
				}
				if strings.HasSuffix(typ, "/3dmodel") {
					if f := find(zr, target); f != nil {
						return f, nil
					}
					return nil, reject("3MF points to the model part %q, which is not in the file", target)
				}
			}
		}
	}
	if f := find(zr, "3D/3dmodel.model"); f != nil {
		return f, nil
	}
	return nil, reject("not a 3MF file: it has no 3D model part (3D/3dmodel.model)")
}

// Parse3MF reads a 3MF package into one welded mesh in millimetres. unit is the unit the file
// declared (so the user can be told what was converted).
func Parse3MF(data []byte, maxTris int) (m *Mesh, unit string, err error) {
	if maxTris <= 0 {
		maxTris = DefaultMaxTriangles
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", reject("not a valid 3MF file (the ZIP container is damaged: %v)", err)
	}
	part, err := modelPart(zr)
	if err != nil {
		return nil, "", err
	}
	xmlData, err := readPart(part)
	if err != nil {
		return nil, "", err
	}
	objects, items, unit, err := parseModelXML(xmlData, maxTris)
	if err != nil {
		return nil, "", err
	}
	if len(items) == 0 {
		return nil, "", reject("3MF has an empty build: nothing to import")
	}
	scale := threeMFUnits[unit]
	w := newWelder(1024)
	instances, tris := 0, 0
	var emit func(id int, t mat, depth int, stack map[int]bool) error
	emit = func(id int, t mat, depth int, stack map[int]bool) error {
		o, ok := objects[id]
		if !ok {
			return reject("3MF refers to object %d, which does not exist", id)
		}
		if depth > maxComposeDeep || stack[id] {
			return reject("3MF components nest too deeply or refer to themselves (object %d)", id)
		}
		if o.typ != "" && o.typ != "model" && o.typ != "other" {
			return reject("3MF object %d is of type %q, not a printable solid", id, o.typ)
		}
		instances++
		if instances > maxInstances {
			return reject("3MF produces more than %d object instances", maxInstances)
		}
		if o.hasMesh {
			flip := t.det() < 0 // a mirroring transform reverses the winding
			ids := make([]int64, len(o.verts))
			for i := range ids {
				ids[i] = -1
			}
			for _, tr := range o.tris {
				if tris++; tris > maxTris {
					return reject("mesh has more than %d triangles, the limit: decimate it first", maxTris)
				}
				var v [3]uint32
				for j, vi := range tr {
					if ids[vi] < 0 {
						p := t.apply(o.verts[vi])
						var c [3]float32
						for k := range c {
							c[k] = float32(p[k] * scale)
							if !okCoord(c[k]) {
								return reject("3MF object %d has a non-finite or absurd coordinate (%v)", id, p[k]*scale)
							}
						}
						ids[vi] = int64(w.add(c[0], c[1], c[2]))
					}
					v[j] = uint32(ids[vi])
				}
				if flip {
					v[1], v[2] = v[2], v[1]
				}
				w.m.Indices = append(w.m.Indices, v[0], v[1], v[2])
			}
		}
		stack[id] = true
		defer delete(stack, id)
		for _, c := range o.components {
			if err := emit(c.object, c.t.then(t), depth+1, stack); err != nil {
				return err
			}
		}
		return nil
	}
	for _, it := range items {
		if err := emit(it.object, it.t, 0, map[int]bool{}); err != nil {
			return nil, "", err
		}
	}
	if tris == 0 {
		return nil, "", reject("3MF contains no triangles")
	}
	return w.m, unit, nil
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func parseModelXML(b []byte, maxTris int) (objects map[int]*tmfObject, items []tmfItem, unit string, err error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	objects = map[int]*tmfObject{}
	unit = "millimeter"
	var cur *tmfObject
	curID := 0
	prefixes := map[string]string{}
	bad := func(format string, a ...any) error { return reject("3MF model: "+format, a...) }
	num := func(se xml.StartElement, name string) (float64, error) {
		v, err := strconv.ParseFloat(attr(se, name), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, bad("<%s> has %s=%q, which is not a number", se.Name.Local, name, attr(se, name))
		}
		return v, nil
	}
	integer := func(se xml.StartElement, name string) (int, error) {
		v, err := strconv.Atoi(attr(se, name))
		if err != nil {
			return 0, bad("<%s> has %s=%q, which is not an integer", se.Name.Local, name, attr(se, name))
		}
		return v, nil
	}
	seenModel := false
	for {
		tok, terr := d.Token()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return nil, nil, "", bad("the XML is malformed (%v)", terr)
		}
		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "model":
				if seenModel {
					return nil, nil, "", bad("more than one <model> element")
				}
				seenModel = true
				for _, a := range se.Attr {
					if a.Name.Space == "xmlns" {
						prefixes[a.Name.Local] = a.Value
					}
				}
				if u := attr(se, "unit"); u != "" {
					if _, ok := threeMFUnits[u]; !ok {
						return nil, nil, "", bad("unknown unit %q", u)
					}
					unit = u
				}
				for _, p := range strings.Fields(attr(se, "requiredextensions")) {
					if ns, ok := prefixes[p]; !ok || !knownExtensions[ns] {
						return nil, nil, "", bad("requires the extension %q, which is not supported (it may change the geometry)", p)
					}
				}
			case "object":
				if !seenModel {
					return nil, nil, "", bad("<object> outside <model>")
				}
				if curID, err = integer(se, "id"); err != nil {
					return nil, nil, "", err
				}
				if _, dup := objects[curID]; dup {
					return nil, nil, "", bad("object id %d is defined twice", curID)
				}
				cur = &tmfObject{typ: attr(se, "type")}
				objects[curID] = cur
			case "vertex":
				if cur == nil {
					return nil, nil, "", bad("<vertex> outside an object")
				}
				var v [3]float64
				for k, n := range []string{"x", "y", "z"} {
					if v[k], err = num(se, n); err != nil {
						return nil, nil, "", err
					}
				}
				cur.verts = append(cur.verts, v)
			case "mesh":
				if cur == nil {
					return nil, nil, "", bad("<mesh> outside an object")
				}
				cur.hasMesh = true
			case "triangle":
				if cur == nil {
					return nil, nil, "", bad("<triangle> outside an object")
				}
				if len(cur.tris) >= maxTris {
					return nil, nil, "", reject("mesh has more than %d triangles, the limit: decimate it first", maxTris)
				}
				var t [3]int
				for k, n := range []string{"v1", "v2", "v3"} {
					if t[k], err = integer(se, n); err != nil {
						return nil, nil, "", err
					}
				}
				cur.tris = append(cur.tris, t)
			case "component":
				if cur == nil {
					return nil, nil, "", bad("<component> outside an object")
				}
				if attr(se, "path") != "" {
					return nil, nil, "", bad("object %d uses a component from another model part (production extension), which is not supported", curID)
				}
				c := tmfComponent{}
				if c.object, err = integer(se, "objectid"); err != nil {
					return nil, nil, "", err
				}
				if c.t, err = parseTransform(attr(se, "transform")); err != nil {
					return nil, nil, "", bad("%v", err)
				}
				cur.components = append(cur.components, c)
			case "item":
				it := tmfItem{}
				if it.object, err = integer(se, "objectid"); err != nil {
					return nil, nil, "", err
				}
				if it.t, err = parseTransform(attr(se, "transform")); err != nil {
					return nil, nil, "", bad("%v", err)
				}
				items = append(items, it)
			}
		case xml.EndElement:
			if se.Name.Local == "object" {
				cur = nil
			}
		}
	}
	if !seenModel {
		return nil, nil, "", bad("no <model> element")
	}
	// Indices are checked once the objects are complete, so a bad one is reported with its object.
	for id, o := range objects {
		for i, t := range o.tris {
			for _, v := range t {
				if v < 0 || v >= len(o.verts) {
					return nil, nil, "", bad("object %d: triangle %d uses vertex %d, but the object has %d vertices", id, i+1, v, len(o.verts))
				}
			}
		}
	}
	return objects, items, unit, nil
}
