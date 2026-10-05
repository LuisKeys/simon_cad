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
