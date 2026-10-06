// Package api is the stable, idiomatic Go geometry API used by the rest of Simon CAD.
// It hides the cgo bridge and guarantees that kernel calls run on a single OS thread.
package api

import (
	"errors"
	"runtime"
	"sync"

	"github.com/accelone/simoncad/internal/geometry/bridge"
)

// ErrReleased is returned when a Shape is used after Release.
var ErrReleased = errors.New("shape already released")

// Props are the mass/bounding properties of a shape (millimetres).
type Props = bridge.Props

// Shape is an owned reference to kernel geometry. Release it when done.
type Shape struct {
	k *Kernel
	h bridge.Handle
}

// Kernel serialises every OCCT call onto one locked OS thread (OCCT is not
// reliably thread-safe) and owns the shapes it creates.
type Kernel struct {
	jobs chan func()
	done chan struct{}
	once sync.Once
}

// New starts a kernel worker.
func New() *Kernel {
	k := &Kernel{jobs: make(chan func()), done: make(chan struct{})}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		for job := range k.jobs {
			job()
		}
		close(k.done)
	}()
	return k
}

// Close stops the worker. Shapes must be released beforehand.
func (k *Kernel) Close() {
	k.once.Do(func() { close(k.jobs) })
	<-k.done
}

// Version returns the OCCT version in use.
func (k *Kernel) Version() string {
	var v string
	k.run(func() { v = bridge.Version() })
	return v
}

// LiveShapes reports shapes not yet released (leak detection).
func (k *Kernel) LiveShapes() int64 {
	var n int64
	k.run(func() { n = bridge.LiveHandles() })
	return n
}

func (k *Kernel) run(fn func()) {
	d := make(chan struct{})
	k.jobs <- func() { fn(); close(d) }
	<-d
}

func (k *Kernel) wrap(fn func() (bridge.Handle, error)) (*Shape, error) {
	var (
		h   bridge.Handle
		err error
	)
	k.run(func() { h, err = fn() })
	if err != nil {
		return nil, err
	}
	return &Shape{k: k, h: h}, nil
}

func (k *Kernel) Box(dx, dy, dz float64) (*Shape, error) {
	return k.wrap(func() (bridge.Handle, error) { return bridge.Box(dx, dy, dz) })
}

func (k *Kernel) Cylinder(radius, height float64) (*Shape, error) {
	return k.wrap(func() (bridge.Handle, error) { return bridge.Cylinder(radius, height) })
}

// Sphere is centered at the origin.
func (k *Kernel) Sphere(radius float64) (*Shape, error) {
	return k.wrap(func() (bridge.Handle, error) { return bridge.Sphere(radius) })
}

// Torus is centered at the origin with its axis along +Z.
func (k *Kernel) Torus(major, minor float64) (*Shape, error) {
	return k.wrap(func() (bridge.Handle, error) { return bridge.Torus(major, minor) })
}

// Cone is a frustum with base radius r1 at z=0 and top radius r2 at z=h.
func (k *Kernel) Cone(r1, r2, h float64) (*Shape, error) {
	return k.wrap(func() (bridge.Handle, error) { return bridge.Cone(r1, r2, h) })
}

// ExtrudePolygon extrudes a closed XY polygon (x,y pairs) from z=0 to z=h.
func (k *Kernel) ExtrudePolygon(xy []float64, h float64) (*Shape, error) {
	pts := append([]float64(nil), xy...)
	return k.wrap(func() (bridge.Handle, error) { return bridge.ExtrudePolygon(pts, h) })
}

func (k *Kernel) Union(a, b *Shape) (*Shape, error) {
	if a.h == 0 || b.h == 0 {
		return nil, ErrReleased
	}
	return k.wrap(func() (bridge.Handle, error) { return bridge.Union(a.h, b.h) })
}

func (k *Kernel) Subtract(a, b *Shape) (*Shape, error) {
	if a.h == 0 || b.h == 0 {
		return nil, ErrReleased
	}
	return k.wrap(func() (bridge.Handle, error) { return bridge.Subtract(a.h, b.h) })
}

// Intersect keeps only the volume common to a and b.
func (k *Kernel) Intersect(a, b *Shape) (*Shape, error) {
	if a.h == 0 || b.h == 0 {
		return nil, ErrReleased
	}
	return k.wrap(func() (bridge.Handle, error) { return bridge.Intersect(a.h, b.h) })
}

func (k *Kernel) ImportSTEP(path string) (*Shape, error) {
	return k.wrap(func() (bridge.Handle, error) { return bridge.ImportSTEP(path) })
}

// Release frees the underlying kernel shape. Safe to call twice.
func (s *Shape) Release() {
	if s == nil || s.h == 0 {
		return
	}
	h := s.h
	s.h = 0
	s.k.run(func() { _ = bridge.Release(h) })
}

func (s *Shape) unary(fn func(bridge.Handle) (bridge.Handle, error)) (*Shape, error) {
	if s.h == 0 {
		return nil, ErrReleased
	}
	return s.k.wrap(func() (bridge.Handle, error) { return fn(s.h) })
}

func (s *Shape) Fillet(radius float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.FilletAllEdges(h, radius) })
}

func (s *Shape) Chamfer(distance float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.ChamferAllEdges(h, distance) })
}

// FilletEdges rounds only the edges with the given kernel indices (as reported by Tessellate).
func (s *Shape) FilletEdges(edges []int, radius float64) (*Shape, error) {
	e := toInt32(edges)
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.FilletEdges(h, e, radius) })
}

// ChamferEdges bevels only the edges with the given kernel indices.
func (s *Shape) ChamferEdges(edges []int, distance float64) (*Shape, error) {
	e := toInt32(edges)
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.ChamferEdges(h, e, distance) })
}

func toInt32(v []int) []int32 {
	out := make([]int32, len(v))
	for i, x := range v {
		out[i] = int32(x)
	}
	return out
}

func (s *Shape) Translate(dx, dy, dz float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.Translate(h, dx, dy, dz) })
}

// Rotate rotates around the axis through (ox,oy,oz) with direction (ax,ay,az); angle in radians.
func (s *Shape) Rotate(ox, oy, oz, ax, ay, az, angle float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) {
		return bridge.Rotate(h, ox, oy, oz, ax, ay, az, angle)
	})
}

// Seg2D is one 2D sketch curve: Kind 0 line (x1,y1,x2,y2), 1 arc through a middle point
// (x1,y1,xm,ym,x2,y2), 2 circle (cx,cy,r).
type Seg2D = bridge.Seg2D

// MakeFace builds a planar face in the XY plane from loops of segments (loop 0 outer, the rest holes).
func (k *Kernel) MakeFace(segs []Seg2D, loopSizes []int) (*Shape, error) {
	ls := toInt32(loopSizes)
	cp := append([]Seg2D(nil), segs...)
	return k.wrap(func() (bridge.Handle, error) { return bridge.MakeFace(cp, ls) })
}

// Prism sweeps the shape (typically a face) along (dx,dy,dz).
func (s *Shape) Prism(dx, dy, dz float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.Prism(h, dx, dy, dz) })
}

// Place moves a shape built in the local XY/+Z frame onto the frame (origin, normal z, x direction).
func (s *Shape) Place(origin, z, x [3]float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.Place(h, origin, z, x) })
}

// Frame is a right-handed placement frame on a planar face.
type Frame struct{ Origin, Normal, X [3]float64 }

// FaceFrame returns the placement frame of the planar face with the kernel index (as in Tessellate).
func (s *Shape) FaceFrame(face int) (Frame, error) {
	if s.h == 0 {
		return Frame{}, ErrReleased
	}
	var (
		f   Frame
		err error
	)
	s.k.run(func() { f.Origin, f.Normal, f.X, err = bridge.FaceFrame(s.h, face) })
	return f, err
}

func (s *Shape) IsValid() (bool, error) {
	if s.h == 0 {
		return false, ErrReleased
	}
	var (
		ok  bool
		err error
	)
	s.k.run(func() { ok, err = bridge.IsValid(s.h) })
	return ok, err
}

func (s *Shape) Props() (Props, error) {
	if s.h == 0 {
		return Props{}, ErrReleased
	}
	var (
		p   Props
		err error
	)
	s.k.run(func() { p, err = bridge.MassProps(s.h) })
	return p, err
}

func (s *Shape) ExportSTEP(path string) error {
	if s.h == 0 {
		return ErrReleased
	}
	var err error
	s.k.run(func() { err = bridge.ExportSTEP(s.h, path) })
	return err
}

// ExportSTL writes a binary STL meshed at the given linear deflection (mm).
func (s *Shape) ExportSTL(path string, deflection float64) error {
	if s.h == 0 {
		return ErrReleased
	}
	var err error
	s.k.run(func() { err = bridge.ExportSTL(s.h, path, deflection, true) })
	return err
}

// Mirror reflects the shape across the plane through (ox,oy,oz) with normal (nx,ny,nz).
func (s *Shape) Mirror(ox, oy, oz, nx, ny, nz float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) {
		return bridge.Mirror(h, ox, oy, oz, nx, ny, nz)
	})
}

// Mesh types are re-exported so callers never import the bridge package.
type (
	Mesh = bridge.Mesh
	Face = bridge.Face
	Edge = bridge.Edge
)

// Surface and curve kinds reported by Mesh faces and edges.
const (
	SurfPlane    = 1
	SurfCylinder = 2
	SurfCone     = 3
	SurfSphere   = 4
	SurfTorus    = 5
	CurveLine    = 1
	CurveCircle  = 2
)

// Tessellate meshes the shape at the given linear deflection (mm). Faces, edges and
// vertices are numbered deterministically, so an index identifies the same entity on
// every rebuild of the same shape.
func (s *Shape) Tessellate(deflection float64) (*Mesh, error) { return s.TessellateAngular(deflection, 0.5) }

// TessellateAngular is Tessellate with an explicit angular deflection (radians): the widest
// angle one segment of a curve may span. Larger values give fewer, longer segments.
func (s *Shape) TessellateAngular(deflection, angular float64) (*Mesh, error) {
	if s.h == 0 {
		return nil, ErrReleased
	}
	var (
		m   *Mesh
		err error
	)
	s.k.run(func() { m, err = bridge.TessellateAngular(s.h, deflection, angular) })
	return m, err
}

// LiveMeshes reports native meshes not yet freed (always 0 outside a Tessellate call).
func (k *Kernel) LiveMeshes() int64 {
	var n int64
	k.run(func() { n = bridge.LiveMeshes() })
	return n
}
