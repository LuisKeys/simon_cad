package features

import (
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/geometry/api"
)

func init() {
	register(&Feature{Type: "box", Build: buildBox})
	register(&Feature{Type: "cylinder", Build: buildCylinder})
	register(&Feature{Type: "union", Build: buildBoolean(false), Target: targetOf})
	register(&Feature{Type: "subtract", Build: buildBoolean(true), Target: targetOf})
	register(&Feature{Type: "fillet", Build: buildEdgeTreatment(true), Target: targetOf})
	register(&Feature{Type: "chamfer", Build: buildEdgeTreatment(false), Target: targetOf})
	register(&Feature{Type: "transform", Build: buildTransform, Target: targetOf})
	register(&Feature{Type: "mirror", Build: buildMirror, Target: targetOf})
}

func buildBox(c *Ctx) (*api.Shape, error) {
	dx, err := c.P.Positive("dx")
	if err != nil {
		return nil, err
	}
	dy, err := c.P.Positive("dy")
	if err != nil {
		return nil, err
	}
	dz, err := c.P.Positive("dz")
	if err != nil {
		return nil, err
	}
	x, _ := c.P.NumOr("x", 0)
	y, _ := c.P.NumOr("y", 0)
	z, _ := c.P.NumOr("z", 0)
	b, err := c.K.Box(dx, dy, dz)
	if err != nil {
		return nil, err
	}
	if x == 0 && y == 0 && z == 0 {
		return b, nil
	}
	defer b.Release()
	return b.Translate(x, y, z)
}

// alignZ rotates a +Z-axis shape so its axis points along the named axis ("x"|"y"|"z").
func alignZ(s *api.Shape, axis string) (*api.Shape, error) {
	switch strings.ToLower(axis) {
	case "", "z":
		return s.Translate(0, 0, 0)
	case "x":
		return s.Rotate(0, 0, 0, 0, 1, 0, math.Pi/2)
	case "y":
		return s.Rotate(0, 0, 0, 1, 0, 0, -math.Pi/2)
	}
	return nil, fmt.Errorf("axis must be x, y or z (got %q)", axis)
}

func buildCylinder(c *Ctx) (*api.Shape, error) {
	r, err := c.P.Positive("radius")
	if err != nil {
		return nil, err
	}
	h, err := c.P.Positive("height")
	if err != nil {
		return nil, err
	}
	x, _ := c.P.NumOr("x", 0)
	y, _ := c.P.NumOr("y", 0)
	z, _ := c.P.NumOr("z", 0)
	cyl, err := c.K.Cylinder(r, h)
	if err != nil {
		return nil, err
	}
	defer cyl.Release()
	aligned, err := alignZ(cyl, c.P.Str("axis"))
	if err != nil {
		return nil, err
	}
	defer aligned.Release()
	return aligned.Translate(x, y, z)
}

func buildBoolean(subtract bool) func(*Ctx) (*api.Shape, error) {
	return func(c *Ctx) (*api.Shape, error) {
		t, err := c.body(c.P.Str("target"))
		if err != nil {
			return nil, err
		}
		tool, err := c.body(c.P.Str("tool"))
		if err != nil {
			return nil, err
		}
		return applyTool(c.K, t.Shape, tool.Shape, subtract)
	}
}

func buildEdgeTreatment(fillet bool) func(*Ctx) (*api.Shape, error) {
	return func(c *Ctx) (*api.Shape, error) {
		t, err := c.body(c.P.Str("target"))
		if err != nil {
			return nil, err
		}
		name := "distance"
		if fillet {
			name = "radius"
		}
		v, err := c.P.Positive(name)
		if err != nil {
			return nil, err
		}
		sz := t.Bounds().Size()
		if limit := math.Min(sz[0], math.Min(sz[1], sz[2])) / 2; v >= limit {
			return nil, fmt.Errorf("%s %g mm is too large for this body (must be < %g mm)", name, v, limit)
		}
		if fillet {
			return t.Shape.Fillet(v)
		}
		return t.Shape.Chamfer(v)
	}
}

func buildTransform(c *Ctx) (*api.Shape, error) {
	t, err := c.body(c.P.Str("target"))
	if err != nil {
		return nil, err
	}
	dx, _ := c.P.NumOr("dx", 0)
	dy, _ := c.P.NumOr("dy", 0)
	dz, _ := c.P.NumOr("dz", 0)
	angle, _ := c.P.NumOr("angle_deg", 0)
	cur, err := t.Shape.Translate(0, 0, 0)
	if err != nil {
		return nil, err
	}
	if angle != 0 {
		ax, _ := c.P.NumOr("ax", 0)
		ay, _ := c.P.NumOr("ay", 0)
		az, _ := c.P.NumOr("az", 1)
		ox, _ := c.P.NumOr("ox", 0)
		oy, _ := c.P.NumOr("oy", 0)
		oz, _ := c.P.NumOr("oz", 0)
		next, err := cur.Rotate(ox, oy, oz, ax, ay, az, degToRad(angle))
		cur.Release()
		if err != nil {
			return nil, err
		}
		cur = next
	}
	if dx != 0 || dy != 0 || dz != 0 {
		next, err := cur.Translate(dx, dy, dz)
		cur.Release()
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

func planeNormal(plane string) (n [3]float64, err error) {
	switch strings.ToUpper(plane) {
	case "XY":
		return [3]float64{0, 0, 1}, nil
	case "XZ":
		return [3]float64{0, 1, 0}, nil
	case "YZ":
		return [3]float64{1, 0, 0}, nil
	}
	return n, fmt.Errorf("plane must be XY, XZ or YZ (got %q)", plane)
}

func buildMirror(c *Ctx) (*api.Shape, error) {
	t, err := c.body(c.P.Str("target"))
	if err != nil {
		return nil, err
	}
	n, err := planeNormal(c.P.Str("plane"))
	if err != nil {
		return nil, err
	}
	off, _ := c.P.NumOr("offset", 0)
	mir, err := t.Shape.Mirror(n[0]*off, n[1]*off, n[2]*off, n[0], n[1], n[2])
	if err != nil {
		return nil, err
	}
	defer mir.Release()
	return c.K.Union(t.Shape, mir)
}
