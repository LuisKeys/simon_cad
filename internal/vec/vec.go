// Package vec is the small 3D vector arithmetic the geometry packages share. A vector is a
// plain [3]float64 (the representation used across the repository), so packages keep their
// own `type Vec = [3]float64` aliases and these functions apply to them directly.
package vec

import "math"

// V is a 3D point or direction.
type V = [3]float64

func Add(a, b V) V             { return V{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func Sub(a, b V) V             { return V{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func Scale(a V, s float64) V   { return V{a[0] * s, a[1] * s, a[2] * s} }
func Dot(a, b V) float64       { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func Len(a V) float64          { return math.Sqrt(Dot(a, a)) }
func Dist(a, b V) float64      { return Len(Sub(a, b)) }
func Lerp(a, b V, t float64) V { return Add(Scale(a, 1-t), Scale(b, t)) }

func Cross(a, b V) V {
	return V{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

// Unit returns a/|a|, or the zero vector when a is (nearly) zero.
func Unit(a V) V {
	n := Len(a)
	if n < 1e-300 {
		return V{}
	}
	return Scale(a, 1/n)
}
