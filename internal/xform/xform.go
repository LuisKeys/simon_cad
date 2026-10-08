// Package xform is the small rotation algebra shared by the geometry layer (a scale in a
// rotated frame) and the dimension service (an object's local axes). It is pure Go with no
// dependencies, so both layers can use it without depending on each other.
package xform

import "math"

// Rot is a rotation matrix mapping local coordinates to world coordinates: world = R·local.
// Its columns are the local X, Y and Z axes expressed in world coordinates.
type Rot [3][3]float64

// Identity is the world frame.
var Identity = Rot{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

const eps = 1e-9

// AxisAngle is the rotation by deg degrees about axis (right-hand rule). A zero axis or a
// zero angle is the identity.
func AxisAngle(axis [3]float64, deg float64) Rot {
	n := math.Sqrt(axis[0]*axis[0] + axis[1]*axis[1] + axis[2]*axis[2])
	if n < eps || deg == 0 {
		return Identity
	}
	x, y, z := axis[0]/n, axis[1]/n, axis[2]/n
	a := deg * math.Pi / 180
	c, s := math.Cos(a), math.Sin(a)
	t := 1 - c
	return Rot{
		{t*x*x + c, t*x*y - s*z, t*x*z + s*y},
		{t*x*y + s*z, t*y*y + c, t*y*z - s*x},
		{t*x*z - s*y, t*y*z + s*x, t*z*z + c},
	}
}

// Mul is r·o: apply o first, then r.
func (r Rot) Mul(o Rot) Rot {
	var out Rot
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			out[i][j] = r[i][0]*o[0][j] + r[i][1]*o[1][j] + r[i][2]*o[2][j]
		}
	}
	return out
}

// T is the transpose, which for a rotation is its inverse (world → local).
func (r Rot) T() Rot {
	var out Rot
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			out[i][j] = r[j][i]
		}
	}
	return out
}

// Apply rotates v.
func (r Rot) Apply(v [3]float64) [3]float64 {
	return [3]float64{
		r[0][0]*v[0] + r[0][1]*v[1] + r[0][2]*v[2],
		r[1][0]*v[0] + r[1][1]*v[1] + r[1][2]*v[2],
		r[2][0]*v[0] + r[2][1]*v[1] + r[2][2]*v[2],
	}
}

// Axis returns local axis i (0 = X, 1 = Y, 2 = Z) in world coordinates.
func (r Rot) Axis(i int) [3]float64 { return [3]float64{r[0][i], r[1][i], r[2][i]} }

// IsIdentity reports whether r is the world frame within tol.
func (r Rot) IsIdentity(tol float64) bool {
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if math.Abs(r[i][j]-Identity[i][j]) > tol {
				return false
			}
		}
	}
	return true
}

// ToAxisAngle decomposes r into a unit axis and an angle in degrees in [0, 180]. The
// identity is reported as axis (0,0,1) and angle 0.
func (r Rot) ToAxisAngle() (axis [3]float64, deg float64) {
	cos := (r[0][0] + r[1][1] + r[2][2] - 1) / 2
	cos = math.Max(-1, math.Min(1, cos))
	a := math.Acos(cos)
	if a < 1e-9 {
		return [3]float64{0, 0, 1}, 0
	}
	if math.Pi-a > 1e-6 {
		s := 2 * math.Sin(a)
		axis = [3]float64{(r[2][1] - r[1][2]) / s, (r[0][2] - r[2][0]) / s, (r[1][0] - r[0][1]) / s}
	} else {
		// Half a turn: the axis comes from the symmetric part, R = 2·n·nᵀ − I.
		x := math.Sqrt(math.Max(0, (r[0][0]+1)/2))
		y := math.Sqrt(math.Max(0, (r[1][1]+1)/2))
		z := math.Sqrt(math.Max(0, (r[2][2]+1)/2))
		switch {
		case x >= y && x >= z:
			y, z = copySign(y, r[0][1]), copySign(z, r[0][2])
		case y >= z:
			x, z = copySign(x, r[0][1]), copySign(z, r[1][2])
		default:
			x, y = copySign(x, r[0][2]), copySign(y, r[1][2])
		}
		axis = [3]float64{x, y, z}
	}
	n := math.Sqrt(axis[0]*axis[0] + axis[1]*axis[1] + axis[2]*axis[2])
	return [3]float64{axis[0] / n, axis[1] / n, axis[2] / n}, a * 180 / math.Pi
}

// copySign gives v the sign of s, treating s = 0 as positive.
func copySign(v, s float64) float64 {
	if s < 0 {
		return -v
	}
	return v
}
