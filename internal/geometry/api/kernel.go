// Package api is the stable, idiomatic Go geometry API used by the rest of Simon CAD.
// It hides the cgo bridge and guarantees that kernel calls run on a single OS thread.
package api

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/accelone/simoncad/internal/geometry/bridge"
)

// ErrReleased is returned when a Shape is used after Release.
var ErrReleased = errors.New("shape already released")

// ErrKernelClosed is returned by operations on a kernel that has been closed.
var ErrKernelClosed = errors.New("the geometry kernel is closed")

// ErrKernelStuck is returned when an OCCT operation did not finish within OpTimeout. The
// worker thread cannot be interrupted, so the call is abandoned and the kernel refuses new
// work until that operation returns (if it ever does).
var ErrKernelStuck = errors.New("the geometry kernel is stuck on a long-running operation")

// OpTimeout bounds how long one kernel operation may run before it is abandoned.
var OpTimeout = 5 * time.Minute

// Props are the mass/bounding properties of a shape (millimetres).
type Props = bridge.Props

// Shape is an owned reference to kernel geometry. Release it when done.
type Shape struct {
	k *Kernel
	// hv is the kernel handle (0 once released). It is atomic so a Release racing with another
	// call is an error from the kernel, not a data race, and so exactly one Release frees it.
	hv atomic.Uint64

	// A shape is immutable, so its mass properties are computed once (they cost milliseconds on
	// swept or traced surfaces and the rebuild, the features' own checks and the tools all ask).
	pmu   sync.Mutex
	props *Props
}

// Kernel serialises every OCCT call onto one locked OS thread (OCCT is not
// reliably thread-safe) and owns the shapes it creates.
type Kernel struct {
	jobs chan func()
	done chan struct{}
	// mu is held for reading while a job is queued and runs, and for writing by Close, so
	// Close waits for work in flight and nothing is ever sent on the closed jobs channel.
	mu     sync.RWMutex
	closed bool
	// stuck is set while an abandoned (timed out) operation still occupies the worker.
	stuck atomic.Bool
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

// Close stops the worker after the work in flight. Shapes must be released beforehand.
// It is safe to call twice; operations on a closed kernel fail with ErrKernelClosed.
func (k *Kernel) Close() {
	k.mu.Lock()
	if !k.closed {
		k.closed = true
		close(k.jobs)
	}
	k.mu.Unlock()
	if k.stuck.Load() {
		return // the worker is wedged in OCCT: do not block shutdown on it
	}
	<-k.done
}

// Version returns the OCCT version in use.
func (k *Kernel) Version() string {
	var v string
	if k.run(func() { v = bridge.Version() }) != nil {
		return ""
	}
	return v
}

// LiveShapes reports shapes not yet released (leak detection).
func (k *Kernel) LiveShapes() int64 {
	var n int64
	if k.run(func() { n = bridge.LiveHandles() }) != nil {
		return 0
	}
	return n
}

// run executes fn on the kernel thread and waits for it. It reports ErrKernelClosed, without
// running fn, when the kernel has been closed.
func (k *Kernel) run(fn func()) error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.closed {
		return ErrKernelClosed
	}
	if k.stuck.Load() {
		return ErrKernelStuck
	}
	d := make(chan struct{})
	var abandoned atomic.Bool
	job := func() {
		fn()
		close(d)
		if abandoned.Load() {
			k.stuck.Store(false) // the abandoned operation finally returned
		}
	}
	timer := time.NewTimer(OpTimeout)
	defer timer.Stop()
	select {
	case k.jobs <- job:
	case <-timer.C:
		return ErrKernelStuck
	}
	// fn may still write the caller's variables after a timeout: on that error they must not be read.
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(OpTimeout)
	select {
	case <-d:
		return nil
	case <-timer.C:
		abandoned.Store(true)
		k.stuck.Store(true)
		select {
		case <-d:
			k.stuck.Store(false)
		default:
		}
		return ErrKernelStuck
	}
}

func (k *Kernel) wrap(fn func() (bridge.Handle, error)) (*Shape, error) {
	var (
		h   bridge.Handle
		err error
	)
	if rerr := k.run(func() { h, err = fn() }); rerr != nil {
		return nil, rerr
	}
	if err != nil {
		return nil, err
	}
	sh := &Shape{k: k}
	sh.hv.Store(uint64(h))
	return sh, nil
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
	if a.handle() == 0 || b.handle() == 0 {
		return nil, ErrReleased
	}
	return k.wrap(func() (bridge.Handle, error) { return bridge.Union(a.handle(), b.handle()) })
}

func (k *Kernel) Subtract(a, b *Shape) (*Shape, error) {
	if a.handle() == 0 || b.handle() == 0 {
		return nil, ErrReleased
	}
	return k.wrap(func() (bridge.Handle, error) { return bridge.Subtract(a.handle(), b.handle()) })
}

// Intersect keeps only the volume common to a and b.
func (k *Kernel) Intersect(a, b *Shape) (*Shape, error) {
	if a.handle() == 0 || b.handle() == 0 {
		return nil, ErrReleased
	}
	return k.wrap(func() (bridge.Handle, error) { return bridge.Intersect(a.handle(), b.handle()) })
}

func (k *Kernel) ImportSTEP(path string) (*Shape, error) {
	return k.wrap(func() (bridge.Handle, error) { return bridge.ImportSTEP(path) })
}

// ImportMesh builds a solid from a welded triangle mesh that has already been validated
// (internal/meshimport). expectedVolume is the mesh's own volume, which the result must match.
func (k *Kernel) ImportMesh(positions []float64, indices []uint32, expectedVolume float64) (*Shape, error) {
	return k.wrap(func() (bridge.Handle, error) { return bridge.ImportMesh(positions, indices, expectedVolume) })
}

func (s *Shape) handle() bridge.Handle { return bridge.Handle(s.hv.Load()) }

// Release frees the underlying kernel shape. Safe to call twice, and from several goroutines:
// exactly one call frees it. After the kernel is closed there is nothing left to free.
func (s *Shape) Release() {
	if s == nil {
		return
	}
	h := bridge.Handle(s.hv.Swap(0))
	if h == 0 {
		return
	}
	_ = s.k.run(func() { _ = bridge.Release(h) })
}

func (s *Shape) unary(fn func(bridge.Handle) (bridge.Handle, error)) (*Shape, error) {
	if s.handle() == 0 {
		return nil, ErrReleased
	}
	return s.k.wrap(func() (bridge.Handle, error) { return fn(s.handle()) })
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
	if s.handle() == 0 {
		return Frame{}, ErrReleased
	}
	var (
		f   Frame
		err error
	)
	if rerr := s.k.run(func() { f.Origin, f.Normal, f.X, err = bridge.FaceFrame(s.handle(), face) }); rerr != nil {
		err = rerr
	}
	return f, err
}

func (s *Shape) IsValid() (bool, error) {
	if s.handle() == 0 {
		return false, ErrReleased
	}
	var (
		ok  bool
		err error
	)
	if rerr := s.k.run(func() { ok, err = bridge.IsValid(s.handle()) }); rerr != nil {
		err = rerr
	}
	return ok, err
}

func (s *Shape) Props() (Props, error) {
	if s.handle() == 0 {
		return Props{}, ErrReleased
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if s.props != nil {
		return *s.props, nil
	}
	var (
		p   Props
		err error
	)
	if rerr := s.k.run(func() { p, err = bridge.MassProps(s.handle()) }); rerr != nil {
		err = rerr
	}
	if err == nil {
		s.props = &p
	}
	return p, err
}

func (s *Shape) ExportSTEP(path string) error {
	if s.handle() == 0 {
		return ErrReleased
	}
	var err error
	if rerr := s.k.run(func() { err = bridge.ExportSTEP(s.handle(), path) }); rerr != nil {
		err = rerr
	}
	return err
}

// ExportSTL writes a binary STL meshed at the given linear deflection (mm).
func (s *Shape) ExportSTL(path string, deflection float64) error {
	if s.handle() == 0 {
		return ErrReleased
	}
	var err error
	if rerr := s.k.run(func() { err = bridge.ExportSTL(s.handle(), path, deflection, true) }); rerr != nil {
		err = rerr
	}
	return err
}

// Mirror reflects the shape across the plane through (ox,oy,oz) with normal (nx,ny,nz).
func (s *Shape) Mirror(ox, oy, oz, nx, ny, nz float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) {
		return bridge.Mirror(h, ox, oy, oz, nx, ny, nz)
	})
}

// Scale scales the shape about (ox,oy,oz) by per-axis factors (all > 0).
func (s *Shape) Scale(ox, oy, oz, sx, sy, sz float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.Scale(h, ox, oy, oz, sx, sy, sz) })
}

// UnifyFaces merges neighbouring faces on the same surface (and collinear edges), e.g. the
// coplanar seams a push/pull union leaves behind.
func (s *Shape) UnifyFaces() (*Shape, error) {
	return s.unary(bridge.UnifyFaces)
}

// Face copies the face with the kernel index (as in Tessellate).
func (s *Shape) Face(face int) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.FaceGet(h, face) })
}

// FaceInset returns the planar face shrunk by d (> 0) from every boundary, as a new face.
func (s *Shape) FaceInset(face int, d float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.FaceInset(h, face, d) })
}

// ImprintInset returns the shape with the face split along its inset outline.
func (s *Shape) ImprintInset(face int, d float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.FaceImprintInset(h, face, d) })
}

// OffsetFace returns the shape with one face moved by d along its outward normal.
func (s *Shape) OffsetFace(face int, d float64) (*Shape, error) {
	return s.unary(func(h bridge.Handle) (bridge.Handle, error) { return bridge.OffsetFace(h, face, d) })
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
func (s *Shape) Tessellate(deflection float64) (*Mesh, error) {
	return s.TessellateAngular(deflection, 0.5)
}

// TessellateAngular is Tessellate with an explicit angular deflection (radians): the widest
// angle one segment of a curve may span. Larger values give fewer, longer segments.
func (s *Shape) TessellateAngular(deflection, angular float64) (*Mesh, error) {
	if s.handle() == 0 {
		return nil, ErrReleased
	}
	var (
		m   *Mesh
		err error
	)
	if rerr := s.k.run(func() { m, err = bridge.TessellateAngular(s.handle(), deflection, angular) }); rerr != nil {
		err = rerr
	}
	return m, err
}

// Topology returns what Tessellate returns for faces, edges and vertices (same numbering and
// exact measures) without triangulating: it has no triangles. Use it when only the entities
// are needed; meshing a face with many holes costs on the order of 100 ms.
func (s *Shape) Topology(deflection float64) (*Mesh, error) {
	if s.handle() == 0 {
		return nil, ErrReleased
	}
	var (
		m   *Mesh
		err error
	)
	if rerr := s.k.run(func() { m, err = bridge.Topology(s.handle(), deflection) }); rerr != nil {
		err = rerr
	}
	return m, err
}

// LiveMeshes reports native meshes not yet freed (always 0 outside a Tessellate call).
func (k *Kernel) LiveMeshes() int64 {
	var n int64
	if k.run(func() { n = bridge.LiveMeshes() }) != nil {
		return 0
	}
	return n
}

// Sweep types are defined by the bridge; callers build them from path frames and a profile.
type (
	SweepSection = bridge.SweepSection
	SweepContour = bridge.SweepContour
	SweepSpec    = bridge.SweepSpec
)

// Sweep lofts a profile through caller-placed sections into a solid.
func (k *Kernel) Sweep(sp SweepSpec) (*Shape, error) {
	cp := sp
	cp.Sections = append([]SweepSection(nil), sp.Sections...)
	cp.Contours = append([]SweepContour(nil), sp.Contours...)
	return k.wrap(func() (bridge.Handle, error) { return bridge.Sweep(cp) })
}
