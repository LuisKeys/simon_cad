package transform

import (
	"fmt"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/dimensions"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
)

// How a motion is written into the CAD-IR for an object.
const (
	// WriteFeature appends a `transform` feature that moves the object's body. The features
	// it is built from (and a component's source project) are never edited.
	WriteFeature = "transform_feature"
	// WriteInstance updates a component instance's own placement: its local transform in the
	// parent project. The component's source definition is untouched.
	WriteInstance = "instance_placement"
)

// Pivots an object can be rotated about. A custom point is reserved for a later phase in the
// UI; the agent tools already accept one.
const (
	PivotOrigin = "origin" // the object's own origin (default)
	PivotCenter = "center" // the center of the object's box (in its local frame)
	PivotPoint  = "point"  // an explicit world point
)

// Pivots lists the pivot kinds.
var Pivots = []string{PivotOrigin, PivotCenter, PivotPoint}

// Object is where an object is and how it can be moved: the answer to getObjectTransform.
type Object struct {
	ID string `json:"object_id"`
	// Origin is the object's own origin and Center its box center, in world mm.
	Origin [3]float64 `json:"origin"`
	Center [3]float64 `json:"center"`
	// Axes are the object's local X, Y, Z in world coordinates; AxisDeg/Axis the same
	// orientation as one rotation (0° when the object is not rotated).
	Axes     [3][3]float64 `json:"axes"`
	Axis     [3]float64    `json:"rotation_axis"`
	AngleDeg float64       `json:"rotation_deg"`
	// Write says how a move or rotation is recorded (WriteFeature or WriteInstance).
	Write string `json:"write"`
	// Component is the referenced project of a component instance ("" otherwise).
	Component string `json:"component,omitempty"`
	// Transformable is false when the object cannot be moved now; Reason says why.
	Transformable bool   `json:"transformable"`
	Reason        string `json:"reason,omitempty"`
	Units         string `json:"units"`
}

// Get reads an object's transform from the rebuilt model. The object must be a visible solid
// body (a leaf of the model); anything else is an error.
func Get(g *cadir.Graph, m *rebuild.Model, id string) (*Object, error) {
	n := g.Get(id)
	if n == nil {
		return nil, fmt.Errorf("feature %q does not exist", id)
	}
	leaf := false
	for _, l := range m.Leaves {
		leaf = leaf || l == id
	}
	if !leaf {
		return nil, fmt.Errorf("feature %q is not a visible body", id)
	}
	d, err := dimensions.Measure(g, m, id)
	if err != nil {
		return nil, err
	}
	o := &Object{ID: id, Origin: d.Origin, Center: d.Center, Axes: d.Axes, Write: WriteFeature,
		Transformable: true, Units: d.Units}
	axis, deg := d.Frame().ToAxisAngle()
	o.Axis, o.AngleDeg = cleanVec(roundVec(axis, axisDecimals), 1e-12), Round(deg, angleDecimals)
	if n.Type == features.TypeComponentInstance {
		o.Write = WriteInstance
		o.Component, _ = n.Parameters[features.ParamComponent].(string)
		// A placement driven by an expression cannot be overwritten by a drag.
		for _, k := range Keys {
			if _, has := n.Expressions[k]; has {
				o.Transformable, o.Reason = false, fmt.Sprintf("its placement %s.%s is driven by an expression", id, k)
				break
			}
		}
	}
	return o, nil
}

// PivotPointOf resolves a pivot kind for an object; point is used for PivotPoint.
func (o *Object) PivotPointOf(kind string, point [3]float64) ([3]float64, error) {
	switch kind {
	case "", PivotOrigin:
		return o.Origin, nil
	case PivotCenter:
		return o.Center, nil
	case PivotPoint:
		return point, nil
	}
	return [3]float64{}, fmt.Errorf("unknown pivot %q (use origin, center or point)", kind)
}

// Compose writes motion m into an existing placement (a component instance's), keeping the
// placement's own pivot when it already rotates, else using pivot. The result is the
// placement that is m after the old one; a component's own scale stays first, as the
// builder applies it, so nonuniform scaling composes correctly.
func Compose(old Placement, m Rigid, pivot [3]float64) Placement {
	if old.AngleDeg != 0 {
		pivot = old.Pivot
	}
	return PlacementOf(old.Rigid().Then(m), pivot)
}

func roundVec(v [3]float64, dec int) [3]float64 {
	return [3]float64{Round(v[0], dec), Round(v[1], dec), Round(v[2], dec)}
}
