// Package bridge is the only package that imports "C". It exposes the OCCT C ABI
// (native/occ/include/occ_bridge.h) as thin Go functions over opaque handles.
// Callers must use the geometry/api package instead of this one.
package bridge

/*
#cgo CFLAGS: -I${SRCDIR}/../../../native/occ/include
#cgo LDFLAGS: -L${SRCDIR}/../../../out/native -lsimon_occ
#cgo LDFLAGS: -L${SRCDIR}/../../../third_party/occt-install/lib -Wl,--disable-new-dtags -Wl,-rpath,${SRCDIR}/../../../third_party/occt-install/lib
#cgo LDFLAGS: -lTKDESTEP -lTKDESTL -lTKMesh -lTKBO -lTKBool -lTKFillet -lTKOffset -lTKTopAlgo -lTKPrim
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

func Mirror(s Handle, ox, oy, oz, nx, ny, nz float64) (Handle, error) {
	var out C.occ_handle
	err := check("mirror", C.occ_mirror(C.occ_handle(s), C.double(ox), C.double(oy), C.double(oz),
		C.double(nx), C.double(ny), C.double(nz), &out))
	return Handle(out), err
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
	var m *C.occ_mesh
	if err := check("tessellate", C.occ_tessellate(C.occ_handle(s), C.double(deflection), &m)); err != nil {
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
