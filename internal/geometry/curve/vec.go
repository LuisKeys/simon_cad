package curve

import "math"

// Vec is a 3D point or direction. The repository uses plain arrays for vectors.
type Vec = [3]float64

func add(a, b Vec) Vec             { return Vec{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func sub(a, b Vec) Vec             { return Vec{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func scale(a Vec, s float64) Vec   { return Vec{a[0] * s, a[1] * s, a[2] * s} }
func dot(a, b Vec) float64         { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func lerp(a, b Vec, t float64) Vec { return add(scale(a, 1-t), scale(b, t)) }
func norm(a Vec) float64           { return math.Sqrt(dot(a, a)) }
func dist(a, b Vec) float64        { return norm(sub(a, b)) }

func cross(a, b Vec) Vec {
	return Vec{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

// unit returns a/|a|, or the zero vector when a is (nearly) zero.
func unit(a Vec) Vec {
	n := norm(a)
	if n < 1e-300 {
		return Vec{}
	}
	return scale(a, 1/n)
}

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
