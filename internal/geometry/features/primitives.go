package features

import (
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/selectors"
)

func init() {
	register(&Feature{Type: "box", Build: buildBox})
	register(&Feature{Type: "cylinder", Build: buildCylinder})
	register(&Feature{Type: "cone", Build: buildCone})
	register(&Feature{Type: "sphere", Build: buildSphere})
	register(&Feature{Type: "torus", Build: buildTorus})
	register(&Feature{Type: "union", Build: buildBoolean(false), Target: targetOf})
	register(&Feature{Type: "subtract", Build: buildBoolean(true), Target: targetOf})
	register(&Feature{Type: "intersect", Build: buildIntersect, Target: targetOf})
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

func buildCone(c *Ctx) (*api.Shape, error) {
	r, err := c.P.Positive("radius")
	if err != nil {
		return nil, err
	}
	top, _ := c.P.NumOr("top_radius", 0)
	if top < 0 {
		return nil, fmt.Errorf("top_radius must be >= 0")
	}
	h, err := c.P.Positive("height")
	if err != nil {
		return nil, err
	}
	x, _ := c.P.NumOr("x", 0)
	y, _ := c.P.NumOr("y", 0)
	z, _ := c.P.NumOr("z", 0)
	cone, err := c.K.Cone(r, top, h)
	if err != nil {
		return nil, err
	}
	defer cone.Release()
	aligned, err := alignZ(cone, c.P.Str("axis"))
	if err != nil {
		return nil, err
	}
	defer aligned.Release()
	return aligned.Translate(x, y, z)
}

// buildSphere centers the sphere at (x,y,z).
func buildSphere(c *Ctx) (*api.Shape, error) {
	r, err := c.P.Positive("radius")
	if err != nil {
		return nil, err
	}
	x, _ := c.P.NumOr("x", 0)
	y, _ := c.P.NumOr("y", 0)
	z, _ := c.P.NumOr("z", 0)
	s, err := c.K.Sphere(r)
	if err != nil {
		return nil, err
	}
	if x == 0 && y == 0 && z == 0 {
		return s, nil
	}
	defer s.Release()
	return s.Translate(x, y, z)
}

// buildTorus centers the torus at (x,y,z) with its axis along `axis`.
func buildTorus(c *Ctx) (*api.Shape, error) {
	major, err := c.P.Positive("major_radius")
	if err != nil {
		return nil, err
	}
	minor, err := c.P.Positive("minor_radius")
	if err != nil {
		return nil, err
	}
	if minor >= major {
		return nil, fmt.Errorf("minor_radius (%g) must be smaller than major_radius (%g)", minor, major)
	}
	x, _ := c.P.NumOr("x", 0)
	y, _ := c.P.NumOr("y", 0)
	z, _ := c.P.NumOr("z", 0)
	t, err := c.K.Torus(major, minor)
	if err != nil {
		return nil, err
	}
	defer t.Release()
	aligned, err := alignZ(t, c.P.Str("axis"))
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

func buildIntersect(c *Ctx) (*api.Shape, error) {
	t, err := c.body(c.P.Str("target"))
	if err != nil {
		return nil, err
	}
	tool, err := c.body(c.P.Str("tool"))
	if err != nil {
		return nil, err
	}
	return c.K.Intersect(t.Shape, tool.Shape)
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
		edges, err := edgeIndices(c.P, c.P.Str("target"), t.Topology())
		if err != nil {
			return nil, err
		}
		sz := t.Bounds().Size()
		if limit := math.Min(sz[0], math.Min(sz[1], sz[2])) / 2; edges == nil && v >= limit {
			return nil, fmt.Errorf("%s %g mm is too large for this body (must be < %g mm)", name, v, limit)
		}
		switch {
		case edges == nil && fillet:
			return t.Shape.Fillet(v)
		case edges == nil:
			return t.Shape.Chamfer(v)
		case fillet:
			return t.Shape.FilletEdges(edges, v)
		}
		return t.Shape.ChamferEdges(edges, v)
	}
}

// edgeIndices reads the optional `edges` parameter: edge(feature=<target>,index=N)
// selectors. nil means every edge.
func edgeIndices(p Params, target string, topo *selectors.Topology) ([]int, error) {
	raw, ok := p["edges"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("edges must be a non-empty list of edge(...) selectors")
	}
	if topo == nil {
		return nil, fmt.Errorf("the target has no resolvable topology")
	}
	seen := map[int]bool{}
	var out []int
	for _, it := range list {
		str, _ := it.(string)
		sel, err := selectors.Parse(str)
		if err != nil {
			return nil, err
		}
		if sel.Kind != "edge" {
			return nil, fmt.Errorf("%s is not an edge selector", sel.String())
		}
		if sel.Feature() != target {
			return nil, fmt.Errorf("edge selector %s is not on the target %q", sel.String(), target)
		}
		if _, err := selectors.ResolveTopo(sel, topo); err != nil {
			return nil, fmt.Errorf("unresolved selector %s: %w", sel.String(), err)
		}
		if i := sel.IndexOf(); !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	return out, nil
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
