// Package visual is the deterministic half of visual closed-loop refinement: an offscreen
// software renderer over the tessellated model (fixed cameras, shaded and silhouette modes),
// reference-image segmentation, silhouette matching (IoU, contour distance, bounding box),
// proportions from landmarks, weighted stage scoring, region ownership/locks and the visual
// iteration history. Nothing here calls a language model or changes CAD geometry.
package visual

import (
	"fmt"
	"math"
)

// Projection kinds.
const (
	Orthographic = "orthographic"
	Perspective  = "perspective"
)

// Camera is a view direction around the model. Azimuth is measured in the XY plane from +X
// (the object's front) towards +Y (its left side); elevation from the XY plane towards +Z.
//
//	front: az 0 · left: az 90 · rear: az 180 · right: az 270 · top: el 90
type Camera struct {
	Name       string  `json:"name"`
	Azimuth    float64 `json:"azimuth"`
	Elevation  float64 `json:"elevation"`
	Projection string  `json:"projection"`
	FOV        float64 `json:"fov,omitempty"` // vertical field of view in degrees (perspective)
	// Scale zooms the framing (1 = the frame fills the image with the standard margin).
	Scale float64 `json:"scale,omitempty"`
}

// Standard views. Their parameters never change between iterations, so renders of
// successive iterations are directly comparable.
var standardViews = map[string]Camera{
	"left":     {Name: "left", Azimuth: 90, Elevation: 0, Projection: Orthographic},
	"right":    {Name: "right", Azimuth: 270, Elevation: 0, Projection: Orthographic},
	"front":    {Name: "front", Azimuth: 0, Elevation: 0, Projection: Orthographic},
	"rear":     {Name: "rear", Azimuth: 180, Elevation: 0, Projection: Orthographic},
	"top":      {Name: "top", Azimuth: 0, Elevation: 90, Projection: Orthographic},
	"front_3q": {Name: "front_3q", Azimuth: 40, Elevation: 18, Projection: Perspective, FOV: 30},
	"rear_3q":  {Name: "rear_3q", Azimuth: 140, Elevation: 18, Projection: Perspective, FOV: 30},
}

// DefaultViews are rendered when none are requested.
var DefaultViews = []string{"left", "front", "top", "front_3q"}

// ViewNames lists the standard views in a stable order.
var ViewNames = []string{"left", "right", "front", "rear", "top", "front_3q", "rear_3q"}

// StandardCamera returns a standard view by name.
func StandardCamera(name string) (Camera, error) {
	c, ok := standardViews[name]
	if !ok {
		return Camera{}, fmt.Errorf("unknown view %q (use %v)", name, ViewNames)
	}
	return c, nil
}

// Frame is the fixed region of space the cameras look at. Keeping it constant across
// iterations keeps the pixel scale constant, so renders can be compared directly.
type Frame struct {
	Center [3]float64 `json:"center"`
	Radius float64    `json:"radius"` // half the diagonal of the framed box
}

// FrameFor frames a bounding box with a margin.
func FrameFor(min, max [3]float64) Frame {
	c := [3]float64{(min[0] + max[0]) / 2, (min[1] + max[1]) / 2, (min[2] + max[2]) / 2}
	d := math.Sqrt(sq(max[0]-min[0]) + sq(max[1]-min[1]) + sq(max[2]-min[2]))
	return Frame{Center: c, Radius: math.Max(d/2, 1)}
}

// Contains reports whether a box fits in the frame.
func (f Frame) Contains(min, max [3]float64) bool {
	for _, p := range [][3]float64{min, max} {
		if math.Sqrt(sq(p[0]-f.Center[0])+sq(p[1]-f.Center[1])+sq(p[2]-f.Center[2])) > f.Radius*1.001 {
			return false
		}
	}
	return true
}

func sq(v float64) float64 { return v * v }

// basis is a camera's orthonormal frame and projection.
type basis struct {
	pos, fwd, right, up [3]float64
	persp               bool
	halfW, halfH        float64 // ortho: world half-extent; perspective: tan(fov/2) scaled
	near                float64
}

const frameMargin = 1.08

func (c Camera) basis(f Frame, aspect float64) basis {
	az, el := c.Azimuth*math.Pi/180, c.Elevation*math.Pi/180
	dir := [3]float64{math.Cos(el) * math.Cos(az), math.Cos(el) * math.Sin(az), math.Sin(el)} // centre → camera
	fwd := [3]float64{-dir[0], -dir[1], -dir[2]}
	worldUp := [3]float64{0, 0, 1}
	if math.Abs(dot(fwd, worldUp)) > 0.999 { // looking straight down/up: +Y is "up" in the image
		worldUp = [3]float64{0, 1, 0}
	}
	right := norm(cross(fwd, worldUp))
	up := cross(right, fwd)
	scale := c.Scale
	if scale <= 0 {
		scale = 1
	}
	b := basis{fwd: fwd, right: right, up: up}
	ext := f.Radius * frameMargin / scale
	if c.Projection == Perspective {
		fov := c.FOV
		if fov <= 0 {
			fov = 30
		}
		t := math.Tan(fov * math.Pi / 360)
		dist := ext / math.Sin(fov*math.Pi/360)
		b.pos = [3]float64{f.Center[0] + dir[0]*dist, f.Center[1] + dir[1]*dist, f.Center[2] + dir[2]*dist}
		b.persp, b.halfH, b.halfW, b.near = true, t, t*aspect, dist*0.01
	} else {
		dist := f.Radius * 4
		b.pos = [3]float64{f.Center[0] + dir[0]*dist, f.Center[1] + dir[1]*dist, f.Center[2] + dir[2]*dist}
		b.halfH, b.halfW = ext, ext*aspect
	}
	return b
}

// project maps a world point to normalized device coordinates (-1..1) and a depth.
func (b basis) project(p [3]float64) (x, y, depth float64, ok bool) {
	v := [3]float64{p[0] - b.pos[0], p[1] - b.pos[1], p[2] - b.pos[2]}
	depth = dot(v, b.fwd)
	if b.persp {
		if depth < b.near {
			return 0, 0, 0, false
		}
		return dot(v, b.right) / depth / b.halfW, dot(v, b.up) / depth / b.halfH, depth, true
	}
	return dot(v, b.right) / b.halfW, dot(v, b.up) / b.halfH, depth, true
}

func dot(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func cross(a, b [3]float64) [3]float64 {
	return [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func norm(a [3]float64) [3]float64 {
	l := math.Sqrt(dot(a, a))
	if l == 0 {
		return a
	}
	return [3]float64{a[0] / l, a[1] / l, a[2] / l}
}

func atanDeg(v float64) float64 { return math.Atan(v) * 180 / math.Pi }
