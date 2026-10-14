package curve

import (
	"math"

	"github.com/accelone/simoncad/internal/vec"
)

// Vec is a 3D point or direction. The repository uses plain arrays for vectors.
type Vec = [3]float64

func add(a, b Vec) Vec             { return vec.Add(a, b) }
func sub(a, b Vec) Vec             { return vec.Sub(a, b) }
func scale(a Vec, s float64) Vec   { return vec.Scale(a, s) }
func dot(a, b Vec) float64         { return vec.Dot(a, b) }
func lerp(a, b Vec, t float64) Vec { return vec.Lerp(a, b, t) }
func norm(a Vec) float64           { return vec.Len(a) }
func dist(a, b Vec) float64        { return vec.Dist(a, b) }

func cross(a, b Vec) Vec { return vec.Cross(a, b) }

// unit returns a/|a|, or the zero vector when a is (nearly) zero.
func unit(a Vec) Vec { return vec.Unit(a) }

// rotateAbout rotates v around the unit axis k by angle (radians), Rodrigues' formula.
func rotateAbout(v, k Vec, angle float64) Vec {
	c, s := math.Cos(angle), math.Sin(angle)
	return add(add(scale(v, c), scale(cross(k, v), s)), scale(k, dot(k, v)*(1-c)))
}

// Perp returns a unit vector perpendicular to the unit vector t.
func Perp(t Vec) Vec {
	a := Vec{1, 0, 0}
	if math.Abs(t[0]) > 0.9 {
		a = Vec{0, 1, 0}
	}
	return unit(cross(t, a))
}
