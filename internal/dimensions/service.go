// Package dimensions measures objects and computes dimension edits. It is the shared core
// of the UI's interactive dimensions and the agent's dimension tools:
//
//   - the DimensionService (this file) measures an object's width, depth and height along
//     its local axes, from the rebuilt model — informational dimensions, read only;
//   - the MeshResizeService core (resize.go) turns a requested dimension into per-axis scale
//     factors (lock proportions, division-by-zero guards, bounds). The tools layer turns
//     those factors into an ordinary `scale` feature, so undo/redo, preview and replay are
//     the ChangeSet machinery every other edit uses.
//
// SimonCAD stays a feature/B-Rep modeler: a dimension edit is a non-destructive scale of the
// body, never a parametric constraint. Parametric dimensions (constraints and relations that
// re-solve the model) are reserved for later and are not modeled here.
//
// Width, depth and height are the extents along the object's local X, Y and Z (Z is up).
// The local frame is the accumulated rotation of the object's own chain of features
// (transform and component placements), so a rotated object is measured and resized along
// its own axes, not along the world's.
package dimensions

import (
	"fmt"
	"math"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
	"github.com/accelone/simoncad/internal/selectors"
	"github.com/accelone/simoncad/internal/xform"
)

// Axis names, indexed by local axis: width = X, depth = Y, height = Z.
var Axes = [3]string{"width", "depth", "height"}

// AxisNames are the accepted names of a dimension: width/depth/height or x/y/z.
var AxisNames = []string{"width", "depth", "height", "x", "y", "z"}

// AxisIndex maps a dimension name to its local axis (0 = X, 1 = Y, 2 = Z).
func AxisIndex(name string) (int, bool) {
	switch name {
	case "width", "x":
		return 0, true
	case "depth", "y":
		return 1, true
	case "height", "z":
		return 2, true
	}
	return 0, false
}

// Dimensions is the measurement of one object.
type Dimensions struct {
	Object string `json:"object_id"`
	// Width, Depth and Height are the extents along the local X, Y and Z axes, in mm.
	Width  float64 `json:"width"`
	Depth  float64 `json:"depth"`
	Height float64 `json:"height"`
	// Axes are the local X, Y and Z axes in world coordinates (the identity when the object
	// is not rotated); Rotated says whether they differ from the world axes.
	Axes    [3][3]float64 `json:"axes"`
	Rotated bool          `json:"rotated"`
	// Min and Max are the object's box in its local frame (world = axes · local).
	Min [3]float64 `json:"min"`
	Max [3]float64 `json:"max"`
	// Center is the box center and Origin the object origin, both in world coordinates.
	Center [3]float64 `json:"center"`
	Origin [3]float64 `json:"origin"`
	// LockProportions is the object's proportional-scaling preference.
	LockProportions bool   `json:"lock_proportions"`
	Units           string `json:"units"`

	frame xform.Rot // exact; Axes is rounded for display
}

// Size returns the extents indexed by local axis.
func (d *Dimensions) Size() [3]float64 { return [3]float64{d.Width, d.Depth, d.Height} }

// Frame returns the local frame as a rotation (world = R·local).
func (d *Dimensions) Frame() xform.Rot { return d.frame }

// Measure is the DimensionService: it measures the body of node id in its local frame.
func Measure(g *cadir.Graph, m *rebuild.Model, id string) (*Dimensions, error) {
	n := g.Get(id)
	if n == nil {
		return nil, fmt.Errorf("feature %q does not exist", id)
	}
	r := m.Nodes[id]
	if r == nil || r.Body == nil || r.Body.Sketch {
		return nil, fmt.Errorf("feature %q has no solid body to measure", id)
	}
	fr := LocalFrame(g, m, id)
	b, ok := m.BoundsInFrame(id, fr)
	if !ok {
		return nil, fmt.Errorf("feature %q could not be measured", id)
	}
	b = selectors.Bounds{Min: r6v(b.Min), Max: r6v(b.Max)}
	d := &Dimensions{Object: id, Min: b.Min, Max: b.Max, Rotated: !fr.IsIdentity(1e-9),
		LockProportions: n.LockProportions, Units: g.Units, frame: fr}
	d.Width, d.Depth, d.Height = r6(b.Max[0]-b.Min[0]), r6(b.Max[1]-b.Min[1]), r6(b.Max[2]-b.Min[2])
	for i := 0; i < 3; i++ {
		d.Axes[i] = r6v(fr.Axis(i))
	}
	d.Center = r6v(fr.Apply(b.Center()))
	d.Origin = r6v(ObjectOrigin(g, m, id))
	if d.Units == "" {
		d.Units = "mm"
	}
	return d, nil
}

// chain is the object's history: the node itself, then the body it modifies, and so on down
// to the feature that created it (root last). It follows `target` (and a copy's `source`), the
// same link that makes a modifying feature the visible body in its target's place.
func chain(g *cadir.Graph, m *rebuild.Model, id string) []string {
	var out []string
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		out = append(out, id)
		n, r := g.Get(id), m.Nodes[id]
		if n == nil || r == nil {
			break
		}
		id = predecessor(n.Type, r.Params)
	}
	return out
}

func predecessor(typ string, p features.Params) string {
	if typ == "copy" {
		return p.Str("source")
	}
	if f, ok := features.Lookup(typ); ok && f.Target != nil {
		return f.Target(p)
	}
	return ""
}

func active(m *rebuild.Model, id string) bool {
	r := m.Nodes[id]
	return r != nil && r.Status != cadir.StatusSuppressed
}

// rotationOf is the rotation a node applies to its body: a transform's or a component
// placement's angle_deg about (ax, ay, az), else none.
func rotationOf(typ string, p features.Params) (rot xform.Rot, pivot [3]float64) {
	if typ != "transform" && typ != features.TypeComponentInstance {
		return xform.Identity, pivot
	}
	deg, _ := p.NumOr("angle_deg", 0)
	ax, _ := p.NumOr("ax", 0)
	ay, _ := p.NumOr("ay", 0)
	az, _ := p.NumOr("az", 1)
	for i, k := range []string{"ox", "oy", "oz"} {
		pivot[i], _ = p.NumOr(k, 0)
	}
	return xform.AxisAngle([3]float64{ax, ay, az}, deg), pivot
}

// LocalFrame is the object's orientation: the rotations of its chain composed in build order.
// Scales, booleans and other edits keep the frame.
func LocalFrame(g *cadir.Graph, m *rebuild.Model, id string) xform.Rot {
	c := chain(g, m, id)
	fr := xform.Identity
	for i := len(c) - 1; i >= 0; i-- {
		if !active(m, c[i]) {
			continue
		}
		rot, _ := rotationOf(g.Get(c[i]).Type, m.Nodes[c[i]].Params)
		fr = rot.Mul(fr)
	}
	return fr
}

// rootPlacement are the creating features whose (x, y, z) is their placement point.
var rootPlacement = map[string]bool{"box": true, "cylinder": true, "cone": true, "sphere": true, "torus": true}

// ObjectOrigin is the object's own origin in world coordinates: the placement point of the
// feature that created it (a primitive's x/y/z, a component's origin), carried through every
// move, rotation, copy offset and scale of its chain. A body created without a placement
// point (an extruded sketch, for instance) uses the center of that first body.
func ObjectOrigin(g *cadir.Graph, m *rebuild.Model, id string) [3]float64 {
	c := chain(g, m, id)
	root := c[len(c)-1]
	rn, rr := g.Get(root), m.Nodes[root]
	var p [3]float64
	switch {
	case rn == nil || rr == nil:
	case rootPlacement[rn.Type]:
		for i, k := range []string{"x", "y", "z"} {
			p[i], _ = rr.Params.NumOr(k, 0)
		}
	case rn.Type == features.TypeComponentInstance: // the component's origin, placed
		p = moved(p, rn.Type, rr.Params)
	default:
		if b, ok := m.Bounds(root); ok {
			p = b.Center()
		}
	}
	for i := len(c) - 2; i >= 0; i-- {
		if !active(m, c[i]) {
			continue
		}
		n, r := g.Get(c[i]), m.Nodes[c[i]]
		switch n.Type {
		case "transform", "copy":
			p = moved(p, n.Type, r.Params)
		case "scale":
			p = scaledPoint(m, p, r.Params)
		}
	}
	return p
}

// moved applies a transform, copy offset or component placement to a point, in the order the
// builders apply them: rotation about (ox, oy, oz), then the translation (dx, dy, dz). A
// component's own scale is about the world origin, which keeps its origin where it is.
func moved(p [3]float64, typ string, prm features.Params) [3]float64 {
	rot, o := rotationOf(typ, prm)
	q := rot.Apply([3]float64{p[0] - o[0], p[1] - o[1], p[2] - o[2]})
	for i, k := range []string{"dx", "dy", "dz"} {
		d, _ := prm.NumOr(k, 0)
		q[i] += o[i] + d
	}
	return q
}

// scaledPoint maps a point through a scale feature, with the anchor computed exactly as the
// builder computes it (from the target's box in the scale's frame).
func scaledPoint(m *rebuild.Model, p [3]float64, prm features.Params) [3]float64 {
	f, err := features.ScaleFactors(prm)
	if err != nil {
		return p
	}
	fr, err := features.ScaleFrame(prm)
	if err != nil {
		return p
	}
	b, ok := m.BoundsInFrame(prm.Str("target"), fr)
	if !ok {
		return p
	}
	a, err := features.ScaleAnchor(prm, b)
	if err != nil {
		return p
	}
	if s := prm.Str("about"); s == "origin" || s == "point" {
		a = fr.T().Apply(a)
	}
	l := fr.T().Apply(p)
	for i := range l {
		l[i] = a[i] + f[i]*(l[i]-a[i])
	}
	return fr.Apply(l)
}

// r6 rounds kernel noise away so measurements are tidy and byte-stable.
func r6(v float64) float64 {
	v = math.Round(v*1e6) / 1e6
	if v == 0 {
		return 0
	}
	return v
}

func r6v(v [3]float64) [3]float64 { return [3]float64{r6(v[0]), r6(v[1]), r6(v[2])} }
