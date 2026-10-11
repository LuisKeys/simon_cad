package features

import (
	"fmt"
	"math"

	"github.com/accelone/simoncad/internal/geometry/api"
)

// ReliefType is the node type that raises (emboss) or recesses (deboss / engrave) a source shape
// on a target body. The source is a sketch (its regions are extruded from the sketch plane) or any
// solid node such as a sweep (the solid itself is the tool). Both stay editable: changing the
// sketch, the path or the sweep regenerates the relief.
const ReliefType = "relief"

// Relief operations. Emboss adds material; deboss and engrave remove it (engrave is deboss for a
// swept path: the name tells the agent and the user what was meant).
const (
	OpEmboss  = "emboss"
	OpDeboss  = "deboss"
	OpEngrave = "engrave"
)

func init() {
	register(&Feature{Type: ReliefType, Build: buildRelief, Target: targetOf})
}

// reliefExtension is how far the tool pokes out of the surface (deboss) or into it (emboss): a
// tool whose face lies exactly on the target's surface makes coplanar faces the boolean handles
// badly, an overlapping one is robust and gives the same result.
const reliefExtension = 0.3

func buildRelief(c *Ctx) (*api.Shape, error) {
	op := c.P.Str("op")
	if op != OpEmboss && op != OpDeboss && op != OpEngrave {
		return nil, fmt.Errorf("op must be emboss, deboss or engrave (got %q)", op)
	}
	tgt, err := c.body(c.P.Str("target"))
	if err != nil {
		return nil, err
	}
	srcID := c.P.Str("source")
	src := c.NodeOf(srcID)
	if src == nil {
		return nil, fmt.Errorf("relief has no source %q", srcID)
	}
	offset, _ := c.P.NumOr("offset", 0)
	var tool *api.Shape
	mirror := c.P.Bool("mirror")
	switch src.Type {
	case "sketch":
		depth, err := c.P.Positive("depth")
		if err != nil {
			return nil, err
		}
		w0, d := offset, depth
		if op == OpEmboss {
			w0, d = offset-reliefExtension, depth+reliefExtension // starts inside the surface
		} else {
			w0, d = offset-depth, depth+reliefExtension // into the material, out through the surface
		}
		tool, err = sketchPrism(c, srcID, c.P.Str("region"), w0, d, c.P.Bool("reverse"))
		if err != nil {
			return nil, err
		}
		if mirror {
			sp0, _ := c.ParamsOf(srcID)
			fr, err := sketchFrame(c, src, sp0)
			if err != nil {
				tool.Release()
				return nil, err
			}
			m, err := tool.Mirror(fr.Origin[0], fr.Origin[1], fr.Origin[2], fr.X[0], fr.X[1], fr.X[2])
			tool.Release()
			if err != nil {
				return nil, err
			}
			tool = m
		}
	default:
		b, ok := c.Bodies[srcID]
		if !ok || b == nil || b.Sketch {
			return nil, fmt.Errorf("relief source %q has no solid geometry (give a sketch, a sweep or another solid)", srcID)
		}
		if mirror || offset != 0 {
			return nil, fmt.Errorf("mirror and offset apply to sketch sources; move or mirror the solid source instead")
		}
		if tool, err = b.Shape.Translate(0, 0, 0); err != nil {
			return nil, err
		}
	}
	defer tool.Release()

	var res *api.Shape
	if op == OpEmboss {
		res, err = c.K.Union(tgt.Shape, tool)
	} else {
		res, err = c.K.Subtract(tgt.Shape, tool)
	}
	if err != nil {
		return nil, Coded(CodeBooleanFailed, "check that the source overlaps the target and is a valid shape", "%s failed: %v", op, err)
	}
	props, err := res.Props()
	if err != nil { // nothing is left of the target
		res.Release()
		return nil, Coded(CodeReliefNoEffect, "reduce the depth or move the source", "the %s removes the whole target", op)
	}
	fail := func(code, hint, f string, a ...any) (*api.Shape, error) {
		res.Release()
		return nil, Coded(code, hint, f, a...)
	}
	eps := 1e-7 * math.Max(1, tgt.Props.Volume)
	switch {
	case props.Solids > tgt.Props.Solids:
		if op == OpEmboss {
			return fail(CodeReliefDisconnected, "move the source onto the surface (check its plane/offset) or raise it so it overlaps the target",
				"the raised shape does not touch the target: the result has %d separate pieces", props.Solids)
		}
		return fail(CodeReliefDisconnected, "reduce the depth: the cut separates part of the target",
			"the %s splits the target into %d pieces", op, props.Solids)
	case props.Solids == 0:
		return fail(CodeReliefNoEffect, "reduce the depth or move the source", "the %s removes the whole target", op)
	case op == OpEmboss && props.Volume <= tgt.Props.Volume+eps:
		return fail(CodeReliefNoEffect, "raise the source above the surface (offset / depth) or check its plane",
			"emboss adds no material: the source lies inside the target")
	case op != OpEmboss && props.Volume >= tgt.Props.Volume-eps:
		return fail(CodeReliefNoEffect, "check that the source overlaps the target: plane, offset and depth decide where it cuts",
			"%s removes no material: the source does not reach into the target", op)
	}
	return res, nil
}
