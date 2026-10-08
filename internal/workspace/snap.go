package workspace

import (
	"math"
	"strconv"
)

// GridService: grid coordinates. A grid point is (u, v, w): u and v are the in-plane offsets
// from the grid origin along the plane's two world axes (XY → X, Y; XZ → X, Z; YZ → Y, Z)
// and w is the offset along the world axis normal to the plane. The grid's axes are always
// world axes, so the transform is an offset and a permutation: exact, with no second
// coordinate system to keep in sync.

// planeAxes are the world axis indices of u, v and w for each plane.
var planeAxes = map[string][3]int{PlaneXY: {0, 1, 2}, PlaneXZ: {0, 2, 1}, PlaneYZ: {1, 2, 0}}

// PlaneAxes returns the world axis indices (0 = X, 1 = Y, 2 = Z) of a plane's u, v and normal.
func PlaneAxes(plane string) [3]int {
	if a, ok := planeAxes[plane]; ok {
		return a
	}
	return planeAxes[PlaneXY]
}

// WorldToGrid expresses a world position (mm) in grid coordinates (mm).
func (g Grid) WorldToGrid(p [3]float64) [3]float64 {
	ax := PlaneAxes(g.Plane)
	var out [3]float64
	for i, a := range ax {
		out[i] = p[a] - g.Origin[a]
	}
	return out
}

// GridToWorld is the inverse of WorldToGrid.
func (g Grid) GridToWorld(q [3]float64) [3]float64 {
	ax := PlaneAxes(g.Plane)
	var out [3]float64
	for i, a := range ax {
		out[a] = q[i] + g.Origin[a]
	}
	return out
}

// SnapService: deterministic snapping in world coordinates. Nothing calls it from the mouse
// yet; it is the calculation interactive transforms will use.

// Context is what a snap is computed against.
type Context struct {
	Settings Settings
	// Bypass suspends snapping for one operation (e.g. while a modifier key is held).
	Bypass bool
}

// clean rounds v to 12 significant digits. Snapping multiplies an integer by a step, which
// in binary floating point can land a hair off the decimal the user expects (3 × 0.1 =
// 0.30000000000000004); cleaning makes the result the same decimal every time, and keeps a
// ratio that is mathematically a half (0.35 / 0.1) from rounding the wrong way.
func clean(v float64) float64 {
	if v == 0 || !finite(v) {
		return v
	}
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 12, 64), 64)
	if r == 0 {
		return 0 // no "-0"
	}
	return r
}

// snapTo rounds v to the nearest multiple of step, halves away from zero, so snapping is
// symmetric for negative and positive coordinates.
func snapTo(v, step float64) float64 {
	if step <= 0 || !finite(v) {
		return v
	}
	return clean(math.Round(clean(v/step)) * step)
}

// GridSnapActive reports whether positions snap to the grid in this context.
func (c Context) GridSnapActive() bool {
	s := c.Settings
	return !c.Bypass && s.Snap.Enabled && s.Snap.Grid && s.Grid.Enabled
}

// TranslationSnapActive reports whether translations snap in this context.
func (c Context) TranslationSnapActive() bool {
	return !c.Bypass && c.Settings.Snap.Enabled && c.Settings.Snap.Translation
}

// RotationSnapActive reports whether rotations snap in this context.
func (c Context) RotationSnapActive() bool {
	return !c.Bypass && c.Settings.Snap.Enabled && c.Settings.Snap.Rotation
}

// SnapPosition moves a world position (mm) to the nearest grid intersection in the grid's
// plane, respecting the grid origin; the offset from the plane is kept. It returns the
// position unchanged (and false) when grid snapping is not active.
func SnapPosition(p [3]float64, c Context) ([3]float64, bool) {
	if !c.GridSnapActive() {
		return p, false
	}
	g := c.Settings.Grid
	q := g.WorldToGrid(p)
	q[0], q[1] = snapTo(q[0], g.Spacing), snapTo(q[1], g.Spacing)
	out := g.GridToWorld(q)
	for i := range out {
		out[i] = clean(out[i])
	}
	return out, true
}

// SnapTranslation rounds each component of a displacement (mm) to the translation
// increment (the grid spacing unless one is set). A displacement has no origin, so the grid
// origin does not shift it.
func SnapTranslation(d [3]float64, c Context) ([3]float64, bool) {
	if !c.TranslationSnapActive() {
		return d, false
	}
	step := c.Settings.TranslationStep()
	for i := range d {
		d[i] = snapTo(d[i], step)
	}
	return d, true
}

// SnapAngle rounds an angle in radians to the nearest multiple of the rotation increment.
// The multiple is found in degrees (the unit the increment is configured in), so 45° steps
// land on exactly 45, 90, 135… before the one conversion back to radians.
func SnapAngle(rad float64, c Context) (float64, bool) {
	if !c.RotationSnapActive() {
		return rad, false
	}
	deg := snapTo(rad*180/math.Pi, c.Settings.Snap.RotationIncrement)
	return deg * math.Pi / 180, true
}
