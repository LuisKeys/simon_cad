package features

import (
	"fmt"

	"github.com/accelone/simoncad/internal/geometry/api"
)

func init() {
	register(&Feature{Type: TypeComponentInstance, Build: buildComponentInstance})
}

// TypeComponentInstance is the node type that references a reusable project (a component)
// instead of duplicating its geometry. Its parameters are the reference (component, version,
// content_hash), the placement (dx/dy/dz, angle_deg about ax/ay/az through ox/oy/oz, sx/sy/sz)
// and the parameter overrides passed down to the component.
const TypeComponentInstance = "component_instance"

// ParamComponent is the parameter holding the referenced project's stable id.
const ParamComponent = "component"

// ParamContentHash pins the instance to a specific state of the component. It is a plain
// parameter, so it is part of the node's content key, the rebuild cache key and the graph
// hash: editing the component and re-pinning is an ordinary, undoable parameter change.
const ParamContentHash = "content_hash"

// ParamVersion selects which version of the component to resolve ("latest" by default).
const ParamVersion = "version"

// ParamOverrides holds the instance's parameter overrides for the component's design parameters.
const ParamOverrides = "overrides"

// Overrides reads the override map off a resolved parameter set.
func Overrides(p Params) map[string]any {
	m, _ := p[ParamOverrides].(map[string]any)
	return m
}

func buildComponentInstance(c *Ctx) (*api.Shape, error) {
	id := c.P.Str(ParamComponent)
	if id == "" {
		return nil, fmt.Errorf("component_instance needs a %q (the component's project id)", ParamComponent)
	}
	if c.Components == nil {
		return nil, fmt.Errorf("no component library is configured, cannot resolve component %q", id)
	}
	cur, err := c.Components.Shape(id, c.P.Str(ParamVersion), c.P.Str(ParamContentHash), Overrides(c.P))
	if err != nil {
		return nil, err
	}
	// From here on cur is owned: every step releases its input, and any failure releases cur.
	step := func(next *api.Shape, err error) error {
		if err != nil {
			cur.Release()
			return err
		}
		cur.Release()
		cur = next
		return nil
	}
	num := func(name string, def float64) (float64, error) { return c.P.NumOr(name, def) }

	sx, err := num("sx", 1)
	if err == nil {
		var sy, sz float64
		if sy, err = num("sy", 1); err == nil {
			sz, err = num("sz", 1)
		}
		if err == nil && (sx != 1 || sy != 1 || sz != 1) {
			for i, v := range [3]float64{sx, sy, sz} {
				if !(v > 0) || v > 1000 {
					err = fmt.Errorf("scale factor %s=%g must be in (0, 1000]", [3]string{"sx", "sy", "sz"}[i], v)
					break
				}
			}
			if err == nil {
				err = step(cur.Scale(0, 0, 0, sx, sy, sz))
			}
		}
	}
	if err != nil {
		cur.Release()
		return nil, err
	}

	angle, err := num("angle_deg", 0)
	if err != nil {
		cur.Release()
		return nil, err
	}
	if angle != 0 {
		ax, _ := c.P.NumOr("ax", 0)
		ay, _ := c.P.NumOr("ay", 0)
		az, _ := c.P.NumOr("az", 1)
		ox, _ := c.P.NumOr("ox", 0)
		oy, _ := c.P.NumOr("oy", 0)
		oz, _ := c.P.NumOr("oz", 0)
		if err := step(cur.Rotate(ox, oy, oz, ax, ay, az, degToRad(angle))); err != nil {
			return nil, err
		}
	}

	dx, _ := c.P.NumOr("dx", 0)
	dy, _ := c.P.NumOr("dy", 0)
	dz, _ := c.P.NumOr("dz", 0)
	if dx != 0 || dy != 0 || dz != 0 {
		if err := step(cur.Translate(dx, dy, dz)); err != nil {
			return nil, err
		}
	}
	return cur, nil
}
