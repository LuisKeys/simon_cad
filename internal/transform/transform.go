// Package transform is the TransformService core: the rigid motions an object can be moved
// and rotated by, how a motion is written into the CAD-IR, and where an object currently is.
// The agent tools (move_object, rotate_object, get_object_transform) and the viewport's
// interactive Move/Rotate both go through it, so a drag and a typed command produce the same
// parameters, the same ChangeSet and the same geometry.
//
// It introduces no second geometry engine: a motion becomes the parameters of an ordinary
// `transform` feature (or a component instance's placement), which the rebuild engine applies
// with the kernel like any other feature. This package is pure math over those parameters.
//
// Conventions: lengths are millimetres and angles radians, in world coordinates. The CAD-IR
// stores rotation angles in degrees (angle_deg); the one conversion happens in Params.
package transform

import (
	"math"
	"strconv"

	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/xform"
)

// Rigid is a rigid motion of world space: p ↦ R·p + T. Composing motions composes the
// matrices exactly, so repeated moves never accumulate Euler-angle error.
type Rigid struct {
	R xform.Rot
	T [3]float64
}

// Identity leaves everything where it is.
var Identity = Rigid{R: xform.Identity}

// Translation moves by d (mm).
func Translation(d [3]float64) Rigid { return Rigid{R: xform.Identity, T: d} }

// Rotation turns by rad radians about axis (right-hand rule) through pivot: p ↦ R·(p − pivot) + pivot.
func Rotation(axis [3]float64, rad float64, pivot [3]float64) Rigid {
	r := xform.AxisAngle(axis, rad*180/math.Pi)
	rp := r.Apply(pivot)
	return Rigid{R: r, T: [3]float64{pivot[0] - rp[0], pivot[1] - rp[1], pivot[2] - rp[2]}}
}

// Apply moves a point.
func (m Rigid) Apply(p [3]float64) [3]float64 {
	q := m.R.Apply(p)
	return [3]float64{q[0] + m.T[0], q[1] + m.T[1], q[2] + m.T[2]}
}

// Then is m followed by n.
func (m Rigid) Then(n Rigid) Rigid {
	t := n.R.Apply(m.T)
	return Rigid{R: n.R.Mul(m.R), T: [3]float64{t[0] + n.T[0], t[1] + n.T[1], t[2] + n.T[2]}}
}

// Inverse undoes m.
func (m Rigid) Inverse() Rigid {
	rt := m.R.T()
	t := rt.Apply(m.T)
	return Rigid{R: rt, T: [3]float64{-t[0], -t[1], -t[2]}}
}

// IsIdentity reports whether m moves nothing (within tol, mm and matrix entries).
func (m Rigid) IsIdentity(tol float64) bool {
	return m.R.IsIdentity(tol) && math.Abs(m.T[0]) <= tol && math.Abs(m.T[1]) <= tol && math.Abs(m.T[2]) <= tol
}

// Placement is a motion written the way the CAD-IR writes one (a transform feature or a
// component placement): a rotation by AngleDeg about Axis through Pivot, then the translation
// D. Zero AngleDeg means no rotation (Axis and Pivot are then irrelevant).
type Placement struct {
	AngleDeg float64
	Axis     [3]float64
	Pivot    [3]float64
	D        [3]float64
}

// Rigid is the motion the placement describes.
func (p Placement) Rigid() Rigid {
	m := Identity
	if p.AngleDeg != 0 {
		m = Rotation(p.Axis, p.AngleDeg*math.Pi/180, p.Pivot)
	}
	return m.Then(Translation(p.D))
}

// PlacementOf expresses m as a rotation about pivot followed by a translation. The pivot is
// free (any point gives the same motion); choosing the one the user rotated about keeps the
// stored parameters readable. A rotation about the negative of a world axis is written about
// the positive axis with a negative angle ("-30° about Z", not "30° about -Z").
func PlacementOf(m Rigid, pivot [3]float64) Placement {
	var p Placement
	if !m.R.IsIdentity(1e-12) {
		axis, deg := m.R.ToAxisAngle()
		for i := range axis {
			if math.Abs(math.Abs(axis[i])-1) < 1e-9 && axis[i] < 0 {
				axis, deg = [3]float64{-axis[0], -axis[1], -axis[2]}, -deg
				break
			}
		}
		p.AngleDeg, p.Axis, p.Pivot = deg, cleanVec(axis, 1e-12), pivot
	}
	// m(x) = R·(x − o) + o + D  ⇒  D = T − o + R·o
	ro := m.R.Apply(p.Pivot)
	for i := range p.D {
		p.D[i] = m.T[i] - p.Pivot[i] + ro[i]
	}
	return p
}

// FromParams reads the placement of a transform feature or a component instance (missing
// values take the builders' defaults: no rotation, axis +Z, pivot and offset 0).
func FromParams(p features.Params) Placement {
	num := func(k string, def float64) float64 { v, _ := p.NumOr(k, def); return v }
	return Placement{
		AngleDeg: num("angle_deg", 0),
		Axis:     [3]float64{num("ax", 0), num("ay", 0), num("az", 1)},
		Pivot:    [3]float64{num("ox", 0), num("oy", 0), num("oz", 0)},
		D:        [3]float64{num("dx", 0), num("dy", 0), num("dz", 0)},
	}
}

// Decimals kept in stored parameters: lengths to 1 nm, angles to 1e-9°, axis components to
// 1e-12. Values the user typed or snapped to (12.5 mm, 45°) are stored exactly; float noise
// from composing motions is not.
const (
	lengthDecimals = 6
	angleDecimals  = 9
	axisDecimals   = 12
)

// Params writes the placement as CAD-IR parameters (lengths mm, angle degrees). Entries that
// are zero (or the default +Z axis) are left out, so a pure move stores only dx/dy/dz; Keys
// lists every key Params may write, for clearing ones that are no longer needed.
func (p Placement) Params() map[string]float64 {
	out := map[string]float64{}
	put := func(k string, v float64, dec int) {
		if v = Round(v, dec); v != 0 {
			out[k] = v
		}
	}
	if deg := Round(p.AngleDeg, angleDecimals); deg != 0 {
		out["angle_deg"] = deg
		put("ax", p.Axis[0], axisDecimals)
		put("ay", p.Axis[1], axisDecimals)
		if az := Round(p.Axis[2], axisDecimals); az != 1 {
			out["az"] = az
		}
		put("ox", p.Pivot[0], lengthDecimals)
		put("oy", p.Pivot[1], lengthDecimals)
		put("oz", p.Pivot[2], lengthDecimals)
	}
	put("dx", p.D[0], lengthDecimals)
	put("dy", p.D[1], lengthDecimals)
	put("dz", p.D[2], lengthDecimals)
	return out
}

// Keys are the parameters that make up a placement.
var Keys = []string{"angle_deg", "ax", "ay", "az", "ox", "oy", "oz", "dx", "dy", "dz"}

// Round rounds v to dec decimals through its decimal representation, so 0.1 stays 0.1
// and never prints as -0.
func Round(v float64, dec int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', dec, 64), 64)
	if r == 0 {
		return 0
	}
	return r
}

func cleanVec(v [3]float64, tol float64) [3]float64 {
	for i := range v {
		if math.Abs(v[i]) < tol {
			v[i] = 0
		}
	}
	return v
}

// AxisVector is the unit vector of a world axis name: "x", "y" or "z" (either case).
func AxisVector(name string) ([3]float64, bool) {
	switch name {
	case "x", "X":
		return [3]float64{1, 0, 0}, true
	case "y", "Y":
		return [3]float64{0, 1, 0}, true
	case "z", "Z":
		return [3]float64{0, 0, 1}, true
	}
	return [3]float64{}, false
}
