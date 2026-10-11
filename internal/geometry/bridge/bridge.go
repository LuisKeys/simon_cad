// Package bridge is the only package that imports "C". It exposes the OCCT C ABI
// (native/occ/include/occ_bridge.h) as thin Go functions over opaque handles.
// Callers must use the geometry/api package instead of this one.
package bridge

/*
#cgo CFLAGS: -I${SRCDIR}/../../../native/occ/include
#cgo LDFLAGS: -L${SRCDIR}/../../../out/native -lsimon_occ
#cgo LDFLAGS: -L${SRCDIR}/../../../third_party/occt-install/lib -Wl,-rpath,${SRCDIR}/../../../third_party/occt-install/lib
#cgo LDFLAGS: -lTKDESTEP -lTKDESTL -lTKMesh -lTKFeat -lTKBO -lTKBool -lTKFillet -lTKOffset -lTKTopAlgo -lTKPrim
#cgo LDFLAGS: -lTKGeomAlgo -lTKGeomBase -lTKBRep -lTKG3d -lTKG2d -lTKMath -lTKXSBase -lTKShHealing -lTKernel -lstdc++
#include <stdlib.h>
#include "occ_bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Handle is an opaque reference to a shape owned by the C++ handle table.
type Handle uint64

// Props mirrors occ_props.
type Props struct {
	Volume, Area                       float64
	CX, CY, CZ                         float64
	MinX, MinY, MinZ, MaxX, MaxY, MaxZ float64
	Solids, Faces, Edges               int
}

// ErrKernel wraps failures reported by the OCCT adapter.
var ErrKernel = errors.New("geometry kernel error")

func check(op string, st C.occ_status) error {
	if st == C.OCC_OK {
		return nil
	}
	return fmt.Errorf("%w: %s: %s (status %d)", ErrKernel, op, C.GoString(C.occ_last_error()), int(st))
}

// Version returns the linked OCCT version string.
func Version() string { return C.GoString(C.occ_version()) }

// LiveHandles returns the number of shapes currently held by the adapter.
func LiveHandles() int64 { return int64(C.occ_live_handle_count()) }

func Release(h Handle) error { return check("release", C.occ_release(C.occ_handle(h))) }

func Box(dx, dy, dz float64) (Handle, error) {
	var out C.occ_handle
	err := check("box", C.occ_box(C.double(dx), C.double(dy), C.double(dz), &out))
	return Handle(out), err
}

func Cylinder(r, h float64) (Handle, error) {
	var out C.occ_handle
	err := check("cylinder", C.occ_cylinder(C.double(r), C.double(h), &out))
	return Handle(out), err
}

func Sphere(r float64) (Handle, error) {
	var out C.occ_handle
	err := check("sphere", C.occ_sphere(C.double(r), &out))
	return Handle(out), err
}

func Torus(major, minor float64) (Handle, error) {
	var out C.occ_handle
	err := check("torus", C.occ_torus(C.double(major), C.double(minor), &out))
	return Handle(out), err
}

func Union(a, b Handle) (Handle, error) {
	var out C.occ_handle
	err := check("union", C.occ_boolean_union(C.occ_handle(a), C.occ_handle(b), &out))
	return Handle(out), err
}

func Subtract(a, b Handle) (Handle, error) {
	var out C.occ_handle
	err := check("subtract", C.occ_boolean_subtract(C.occ_handle(a), C.occ_handle(b), &out))
	return Handle(out), err
}

func Intersect(a, b Handle) (Handle, error) {
	var out C.occ_handle
	err := check("intersect", C.occ_boolean_intersect(C.occ_handle(a), C.occ_handle(b), &out))
	return Handle(out), err
}

func FilletAllEdges(s Handle, radius float64) (Handle, error) {
	var out C.occ_handle
	err := check("fillet", C.occ_fillet_all_edges(C.occ_handle(s), C.double(radius), &out))
	return Handle(out), err
}

func edgeCall(op string, s Handle, edges []int32, size float64,
	fn func(C.occ_handle, *C.int32_t, C.int32_t, C.double, *C.occ_handle) C.occ_status) (Handle, error) {
	var out C.occ_handle
	var p *C.int32_t
	if len(edges) > 0 {
		p = (*C.int32_t)(unsafe.Pointer(&edges[0]))
	}
	err := check(op, fn(C.occ_handle(s), p, C.int32_t(len(edges)), C.double(size), &out))
	return Handle(out), err
}

// FilletEdges rounds the edges at the given kernel indices.
func FilletEdges(s Handle, edges []int32, radius float64) (Handle, error) {
	return edgeCall("fillet edges", s, edges, radius, func(h C.occ_handle, e *C.int32_t, n C.int32_t, r C.double, o *C.occ_handle) C.occ_status {
		return C.occ_fillet_edges(h, e, n, r, o)
	})
}

// ChamferEdges bevels the edges at the given kernel indices.
func ChamferEdges(s Handle, edges []int32, d float64) (Handle, error) {
	return edgeCall("chamfer edges", s, edges, d, func(h C.occ_handle, e *C.int32_t, n C.int32_t, r C.double, o *C.occ_handle) C.occ_status {
		return C.occ_chamfer_edges(h, e, n, r, o)
	})
}

func ChamferAllEdges(s Handle, d float64) (Handle, error) {
	var out C.occ_handle
	err := check("chamfer", C.occ_chamfer_all_edges(C.occ_handle(s), C.double(d), &out))
	return Handle(out), err
}

func Translate(s Handle, dx, dy, dz float64) (Handle, error) {
	var out C.occ_handle
	err := check("translate", C.occ_translate(C.occ_handle(s), C.double(dx), C.double(dy), C.double(dz), &out))
	return Handle(out), err
}

func Rotate(s Handle, ox, oy, oz, ax, ay, az, angle float64) (Handle, error) {
	var out C.occ_handle
	err := check("rotate", C.occ_rotate(C.occ_handle(s), C.double(ox), C.double(oy), C.double(oz),
		C.double(ax), C.double(ay), C.double(az), C.double(angle), &out))
	return Handle(out), err
}

func IsValid(s Handle) (bool, error) {
	var v C.int32_t
	err := check("is_valid", C.occ_is_valid(C.occ_handle(s), &v))
	return v == 1, err
}

func MassProps(s Handle) (Props, error) {
	var p C.occ_props
	if err := check("mass_props", C.occ_mass_props(C.occ_handle(s), &p)); err != nil {
		return Props{}, err
	}
	return Props{
		Volume: float64(p.volume), Area: float64(p.area),
		CX: float64(p.cx), CY: float64(p.cy), CZ: float64(p.cz),
		MinX: float64(p.min_x), MinY: float64(p.min_y), MinZ: float64(p.min_z),
		MaxX: float64(p.max_x), MaxY: float64(p.max_y), MaxZ: float64(p.max_z),
		Solids: int(p.solid_count), Faces: int(p.face_count), Edges: int(p.edge_count),
	}, nil
}

func ExportSTEP(s Handle, path string) error {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	return check("export_step", C.occ_export_step(C.occ_handle(s), cs))
}

func ExportSTL(s Handle, path string, deflection float64, binary bool) error {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	var b C.int32_t
	if binary {
		b = 1
	}
	return check("export_stl", C.occ_export_stl(C.occ_handle(s), cs, C.double(deflection), b))
}

func ImportSTEP(path string) (Handle, error) {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	var out C.occ_handle
	err := check("import_step", C.occ_import_step(cs, &out))
	return Handle(out), err
}

func Cone(r1, r2, h float64) (Handle, error) {
	var out C.occ_handle
	err := check("cone", C.occ_cone(C.double(r1), C.double(r2), C.double(h), &out))
	return Handle(out), err
}

// ExtrudePolygon extrudes the closed polygon (x,y pairs in the XY plane) along +Z.
func ExtrudePolygon(xy []float64, h float64) (Handle, error) {
	if len(xy) < 6 || len(xy)%2 != 0 {
		return 0, fmt.Errorf("%w: polygon needs at least 3 points", ErrKernel)
	}
	var out C.occ_handle
	err := check("extrude_polygon", C.occ_extrude_polygon((*C.double)(unsafe.Pointer(&xy[0])), C.int32_t(len(xy)/2), C.double(h), &out))
	return Handle(out), err
}

// Seg2D mirrors occ_seg2d: kind 0 line (x1,y1,x2,y2), 1 arc (x1,y1,xm,ym,x2,y2), 2 circle (cx,cy,r),
// 3 cubic bezier (x1,y1,c1x,c1y,c2x,c2y,x2,y2).
type Seg2D struct {
	Kind int32
	A    [8]float64
}

// MakeFace builds a planar face (XY plane, +Z normal) from loops of segments; loop 0 is the outer boundary.
func MakeFace(segs []Seg2D, loopSizes []int32) (Handle, error) {
	if len(segs) == 0 || len(loopSizes) == 0 {
		return 0, fmt.Errorf("%w: sketch needs at least one loop", ErrKernel)
	}
	cs := make([]C.occ_seg2d, len(segs))
	for i, sg := range segs {
		cs[i].kind = C.int32_t(sg.Kind)
		for j := 0; j < 8; j++ {
			cs[i].a[j] = C.double(sg.A[j])
		}
	}
	var out C.occ_handle
	err := check("make_face", C.occ_make_face(&cs[0], (*C.int32_t)(unsafe.Pointer(&loopSizes[0])), C.int32_t(len(loopSizes)), &out))
	return Handle(out), err
}

// Prism sweeps a shape along (dx,dy,dz).
func Prism(s Handle, dx, dy, dz float64) (Handle, error) {
	var out C.occ_handle
	err := check("prism", C.occ_prism(C.occ_handle(s), C.double(dx), C.double(dy), C.double(dz), &out))
	return Handle(out), err
}

// Place moves a shape from the local XY/+Z frame onto the frame (origin o, normal z, x direction x).
func Place(s Handle, o, z, x [3]float64) (Handle, error) {
	var out C.occ_handle
	err := check("place", C.occ_place(C.occ_handle(s), (*C.double)(&o[0]), (*C.double)(&z[0]), (*C.double)(&x[0]), &out))
	return Handle(out), err
}

// FaceFrame returns origin, outward normal and x direction of a planar face.
func FaceFrame(s Handle, face int) (origin, normal, xdir [3]float64, err error) {
	var buf [9]C.double
	err = check("face_frame", C.occ_face_frame(C.occ_handle(s), C.int32_t(face), &buf[0]))
	for i := 0; i < 3; i++ {
		origin[i], normal[i], xdir[i] = float64(buf[i]), float64(buf[3+i]), float64(buf[6+i])
	}
	return
}

func Mirror(s Handle, ox, oy, oz, nx, ny, nz float64) (Handle, error) {
	var out C.occ_handle
	err := check("mirror", C.occ_mirror(C.occ_handle(s), C.double(ox), C.double(oy), C.double(oz),
		C.double(nx), C.double(ny), C.double(nz), &out))
	return Handle(out), err
}

// Scale scales a shape about (ox,oy,oz) by per-axis factors.
func Scale(s Handle, ox, oy, oz, sx, sy, sz float64) (Handle, error) {
	var out C.occ_handle
	err := check("scale", C.occ_scale(C.occ_handle(s), C.double(ox), C.double(oy), C.double(oz),
		C.double(sx), C.double(sy), C.double(sz), &out))
	return Handle(out), err
}

func faceOp(op string, fn func(C.occ_handle, C.int32_t, C.double, *C.occ_handle) C.occ_status, s Handle, face int, d float64) (Handle, error) {
	var out C.occ_handle
	err := check(op, fn(C.occ_handle(s), C.int32_t(face), C.double(d), &out))
	return Handle(out), err
}

// UnifyFaces merges same-domain neighbouring faces and edges.
func UnifyFaces(s Handle) (Handle, error) {
	var out C.occ_handle
	err := check("unify_faces", C.occ_unify_faces(C.occ_handle(s), &out))
	return Handle(out), err
}

// FaceGet copies one face of a shape.
func FaceGet(s Handle, face int) (Handle, error) {
	var out C.occ_handle
	err := check("face_get", C.occ_face_get(C.occ_handle(s), C.int32_t(face), &out))
	return Handle(out), err
}

// FaceInset is the planar face shrunk by d from its boundary.
func FaceInset(s Handle, face int, d float64) (Handle, error) {
	return faceOp("face_inset", func(h C.occ_handle, i C.int32_t, v C.double, o *C.occ_handle) C.occ_status {
		return C.occ_face_inset(h, i, v, o)
	}, s, face, d)
}

// FaceImprintInset splits the face along its inset outline.
func FaceImprintInset(s Handle, face int, d float64) (Handle, error) {
	return faceOp("face_imprint_inset", func(h C.occ_handle, i C.int32_t, v C.double, o *C.occ_handle) C.occ_status {
		return C.occ_face_imprint_inset(h, i, v, o)
	}, s, face, d)
}

// OffsetFace moves one face along its outward normal by d.
func OffsetFace(s Handle, face int, d float64) (Handle, error) {
	return faceOp("offset_face", func(h C.occ_handle, i C.int32_t, v C.double, o *C.occ_handle) C.occ_status {
		return C.occ_offset_face(h, i, v, o)
	}, s, face, d)
}

// Face, Edge and Mesh mirror occ_face_info, occ_edge_info and the occ_mesh buffers.
type Face struct {
	Surface            int
	Normal, Centroid   [3]float64
	Area, Radius       float64
	Min, Max           [3]float64
	TriStart, TriCount int
}

type Edge struct {
	Curve            int
	Length           float64
	Start, End       [3]float64
	Min, Max         [3]float64
	PtStart, PtCount int
}

type Mesh struct {
	Positions, Normals []float32
	Indices            []uint32
	Faces              []Face
	Edges              []Edge
	EdgePoints         []float32
	Vertices           []float32 // topological vertices, 3 floats each
}

// Tessellate meshes a shape and copies the result out, freeing the native mesh.
func Tessellate(s Handle, deflection float64) (*Mesh, error) {
	return TessellateAngular(s, deflection, 0.5)
}

// TessellateAngular is Tessellate with an explicit angular deflection (radians).
func TessellateAngular(s Handle, deflection, angular float64) (*Mesh, error) {
	var m *C.occ_mesh
	if err := check("tessellate", C.occ_tessellate_ex(C.occ_handle(s), C.double(deflection), C.double(angular), &m)); err != nil {
		return nil, err
	}
	defer C.occ_mesh_free(m)
	var c C.occ_mesh_counts
	if err := check("mesh counts", C.occ_mesh_get_counts(m, &c)); err != nil {
		return nil, err
	}
	out := &Mesh{
		Positions:  make([]float32, 3*int(c.vertices)),
		Normals:    make([]float32, 3*int(c.vertices)),
		Indices:    make([]uint32, 3*int(c.triangles)),
		EdgePoints: make([]float32, 3*int(c.edge_points)),
		Vertices:   make([]float32, 3*int(c.topo_vertices)),
	}
	cf := make([]C.occ_face_info, int(c.faces))
	ce := make([]C.occ_edge_info, int(c.edges))
	// Go-allocated buffers may be passed to C when they hold no Go pointers; empty slices
	// have no &s[0], so route those through nil.
	pf32 := func(s []float32) *C.float {
		if len(s) == 0 {
			return nil
		}
		return (*C.float)(unsafe.Pointer(&s[0]))
	}
	var pi *C.uint32_t
	if len(out.Indices) > 0 {
		pi = (*C.uint32_t)(unsafe.Pointer(&out.Indices[0]))
	}
	var pcf *C.occ_face_info
	if len(cf) > 0 {
		pcf = &cf[0]
	}
	var pce *C.occ_edge_info
	if len(ce) > 0 {
		pce = &ce[0]
	}
	if err := check("mesh copy", C.occ_mesh_copy(m, pf32(out.Positions), pf32(out.Normals), pi, pcf, pce,
		pf32(out.EdgePoints), pf32(out.Vertices))); err != nil {
		return nil, err
	}
	for _, f := range cf {
		out.Faces = append(out.Faces, Face{
			Surface: int(f.surface), Normal: [3]float64{float64(f.nx), float64(f.ny), float64(f.nz)},
			Centroid: [3]float64{float64(f.cx), float64(f.cy), float64(f.cz)}, Area: float64(f.area), Radius: float64(f.radius),
			Min:      [3]float64{float64(f.min_x), float64(f.min_y), float64(f.min_z)},
			Max:      [3]float64{float64(f.max_x), float64(f.max_y), float64(f.max_z)},
			TriStart: int(f.tri_start), TriCount: int(f.tri_count)})
	}
	for _, e := range ce {
		out.Edges = append(out.Edges, Edge{
			Curve: int(e.curve), Length: float64(e.length),
			Start: [3]float64{float64(e.sx), float64(e.sy), float64(e.sz)}, End: [3]float64{float64(e.ex), float64(e.ey), float64(e.ez)},
			Min:     [3]float64{float64(e.min_x), float64(e.min_y), float64(e.min_z)},
			Max:     [3]float64{float64(e.max_x), float64(e.max_y), float64(e.max_z)},
			PtStart: int(e.pt_start), PtCount: int(e.pt_count)})
	}
	return out, nil
}

// LiveMeshes returns native meshes not yet freed (leak detection).
func LiveMeshes() int64 { return int64(C.occ_live_mesh_count()) }

// SweepSection mirrors occ_section: a frame (origin, in-plane axes X and Y; the normal X×Y is the
// path tangent) plus the profile scale applied before placement.
type SweepSection struct {
	Origin, X, Y [3]float64
	SX, SY       float64
}

// SweepContour is one cross-section loop: an ellipse (RX, RY) or a polygon of Points.
type SweepContour struct {
	Ellipse bool
	Hole    bool
	RX, RY  float64
	// Points is one polygon shared by every section; PerSection (one polygon per section, equal
	// lengths) takes precedence and is used by mitered polyline sweeps.
	Points     [][2]float64
	PerSection [][][2]float64
}

// SweepSpec is the input of occ_sweep.
type SweepSpec struct {
	Sections           []SweepSection
	Contours           []SweepContour
	Closed             bool
	ApexStart, ApexEnd *[3]float64
	Ruled              bool
	// Leading / trailing sections that are cap rings running to the apex, and whether each cap
	// is joined with straight bands (a cone).
	CapStartN, CapEndN         int
	CapStartRuled, CapEndRuled bool
}

// Sweep lofts the contours through the sections.
func Sweep(sp SweepSpec) (Handle, error) {
	if len(sp.Sections) == 0 || len(sp.Contours) == 0 {
		return 0, fmt.Errorf("%w: sweep needs sections and a contour", ErrKernel)
	}
	secs := make([]C.occ_section, len(sp.Sections))
	for i, s := range sp.Sections {
		for k := 0; k < 3; k++ {
			secs[i].o[k], secs[i].x[k], secs[i].y[k] = C.double(s.Origin[k]), C.double(s.X[k]), C.double(s.Y[k])
		}
		secs[i].sx, secs[i].sy = C.double(s.SX), C.double(s.SY)
	}
	cons := make([]C.occ_contour, len(sp.Contours))
	var xy []float64
	for i, c := range sp.Contours {
		if c.Ellipse {
			cons[i].kind = 1
		}
		if c.Hole {
			cons[i].hole = 1
		}
		cons[i].n = C.int32_t(len(c.Points))
		cons[i].rx, cons[i].ry = C.double(c.RX), C.double(c.RY)
		for _, p := range c.Points {
			xy = append(xy, p[0], p[1])
		}
		if c.PerSection != nil {
			cons[i].kind = 2
			cons[i].n = C.int32_t(len(c.PerSection[0]))
			xy = xy[:len(xy)-2*len(c.Points)]
			for _, poly := range c.PerSection {
				for _, p := range poly {
					xy = append(xy, p[0], p[1])
				}
			}
		}
	}
	var xyp *C.double
	if len(xy) > 0 {
		xyp = (*C.double)(unsafe.Pointer(&xy[0]))
	}
	var a0, a1 *C.double
	var b0, b1 [3]C.double
	if sp.ApexStart != nil {
		for k := 0; k < 3; k++ {
			b0[k] = C.double(sp.ApexStart[k])
		}
		a0 = &b0[0]
	}
	if sp.ApexEnd != nil {
		for k := 0; k < 3; k++ {
			b1[k] = C.double(sp.ApexEnd[k])
		}
		a1 = &b1[0]
	}
	closed, ruled := C.int32_t(0), C.int32_t(0)
	if sp.Closed {
		closed = 1
	}
	if sp.Ruled {
		ruled = 1
	}
	b2i := func(b bool) C.int32_t {
		if b {
			return 1
		}
		return 0
	}
	var out C.occ_handle
	err := check("sweep", C.occ_sweep(&secs[0], C.int32_t(len(secs)), &cons[0], C.int32_t(len(cons)), xyp, closed, a0, a1, ruled,
		C.int32_t(sp.CapStartN), C.int32_t(sp.CapEndN), b2i(sp.CapStartRuled), b2i(sp.CapEndRuled), &out))
	return Handle(out), err
}
