package tools

import (
	"fmt"
	"strings"

	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/selectors"
)

var entitySchema = object(props{
	"id":   text("Entity id, unique in the sketch (e1, e2, ...)"),
	"kind": enum("Entity kind", "line", "arc", "bezier", "curve", "circle", "rectangle", "polygon", "slot"),
	"p1":   array("line/arc start [u,v]", number("coordinate"), 2, 2),
	"p2":   array("line/arc end [u,v]", number("coordinate"), 2, 2),
	"mid":  array("arc: any point on the arc between p1 and p2 [u,v]", number("coordinate"), 2, 2),
	"c1":   array("bezier: first control point [u,v]", number("coordinate"), 2, 2),
	"c2":   array("bezier: second control point [u,v]", number("coordinate"), 2, 2),
	"segments": array("curve: closed loop of cubic bezier segments [[p1,c1,c2,p2],...]; each starts where the previous ends",
		array("[p1,c1,c2,p2]", array("[u,v]", number("coordinate"), 2, 2), 4, 4), 1, 2000),
	"cx": number("circle center u"), "cy": number("circle center v"), "r": positive("circle radius"),
	"x": number("rectangle min-corner u"), "y": number("rectangle min-corner v"),
	"w": positive("rectangle size along u"), "h": positive("rectangle size along v"),
	"points": array("polygon vertices [[u,v],...]", array("[u,v]", number("coordinate"), 2, 2), 3, 500),
	"x1":     number("slot start u"), "y1": number("slot start v"), "x2": number("slot end u"), "y2": number("slot end v"),
	"width": positive("slot width"),
}, "id", "kind")

var regionSchema = object(props{
	"id":    text("Region id (r1, r2, ...)"),
	"outer": array("Entity ids forming the outer loop (one closed entity, or line/arc entities that chain into a loop)", text("entity id"), 1, 500),
	"holes": array("Inner loops cut out of the region", array("entity ids of one loop", text("entity id"), 1, 500), 0, 200),
}, "id", "outer")

func sketchTools() []*Tool {
	return []*Tool{
		{
			Name: "create_sketch",
			Description: "Create a planar 2D sketch (no solid yet). Entities (mm, plane coordinates u,v): line{p1,p2}, arc{p1,mid,p2}, bezier{p1,c1,c2,p2}, curve{segments: closed bezier loop}, circle{cx,cy,r}, rectangle{x,y,w,h}, polygon{points}, slot{x1,y1,x2,y2,width}. " +
				"`regions` group closed loops into faces (outer loop + holes); if omitted every closed entity (circle/rectangle/polygon/slot) is its own region. " +
				"Place it on plane XY (normal +Z, u=X,v=Y), YZ (normal +X, u=Y,v=Z) or XZ (normal +Y, u=Z,v=X) with `offset`, or on a planar `face` (origin at the face centroid, u along its longest straight edge). " +
				"Turn regions into material with extrude_sketch.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"plane":    enum("Sketch plane", "XY", "XZ", "YZ"),
				"offset":   number("Offset along the sketch normal (mm)"),
				"face":     text("Planar face selector to draw on, e.g. face(feature=plate,side=+Z); overrides plane"),
				"entities": array("Sketch entities", entitySchema, 1, 500),
				"regions":  array("Closed regions (outer loop + holes)", regionSchema, 1, 200),
			}), "entities"),
			Plan: planCreateSketch,
		},
		{
			Name: "extrude_sketch",
			Description: "Extrude regions of an existing sketch. mode=add makes a solid (merged into `target` if given); mode=cut removes material from `target` (a cut from a sketch drawn on a face goes into the material). " +
				"`region` picks one region (default: all). Use through=true for a cut through everything, or symmetric=true to extrude half the depth each way.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"sketch":    text("Sketch feature id"),
				"region":    text("Region id (default: all regions)"),
				"mode":      enum("add or cut", "add", "cut"),
				"target":    text("Feature to add to / cut from"),
				"depth":     positive("Extrusion depth (mm); not needed for through cuts"),
				"symmetric": boolean("Extrude half the depth each way about the sketch plane"),
				"through":   boolean("Cut through the entire target"),
				"reverse":   boolean("Extrude the opposite way"),
			}), "sketch"),
			Plan: planExtrudeSketch,
		},
	}
}

func planCreateSketch(env Env, a Args) (*Plan, error) {
	p := map[string]any{"entities": a["entities"]}
	copyOpt(p, a, "plane", "offset", "regions")
	if err := features.ValidateSketch(features.Params(p)); err != nil {
		return nil, err
	}
	id, err := uniqueID(env.Graph(), a, "sketch")
	if err != nil {
		return nil, err
	}
	n := newNode(env, a, id, "sketch", p)
	if f := a.Str("face"); f != "" {
		sel, err := selectors.Parse(f)
		if err != nil {
			return nil, err
		}
		if sel.Kind != "face" {
			return nil, fmt.Errorf("face must be a face selector, got %q", f)
		}
		d, err := resolveSelector(env, sel)
		if err != nil {
			return nil, err
		}
		if d.SurfaceType != "plane" {
			return nil, fmt.Errorf("%s is not a planar face (%s)", f, d.SurfaceType)
		}
		n.References = map[string]string{"face": sel.String()}
		n.Dependencies = []string{sel.Feature()}
		delete(p, "plane")
	}
	return addPlan(intentOr(a, "Create a sketch"), "Creates a planar sketch.", n), nil
}

func planExtrudeSketch(env Env, a Args) (*Plan, error) {
	sk, err := requireFeature(env, a.Str("sketch"))
	if err != nil {
		return nil, err
	}
	if sk.Type != "sketch" {
		return nil, fmt.Errorf("%q is a %s, not a sketch", sk.ID, sk.Type)
	}
	if r := a.Str("region"); r != "" {
		ids, err := features.SketchRegionIDs(features.Params(sk.Parameters))
		if err != nil {
			return nil, err
		}
		found := false
		for _, x := range ids {
			found = found || x == r
		}
		if !found {
			return nil, fmt.Errorf("sketch %q has no region %q (regions: %s)", sk.ID, r, strings.Join(ids, ", "))
		}
	}
	mode := orDefault(a.Str("mode"), "add")
	if mode == "cut" && a.Str("target") == "" {
		return nil, fmt.Errorf("a cut needs a `target`")
	}
	if t := a.Str("target"); t != "" {
		if _, err := requireBounds(env, t); err != nil {
			return nil, err
		}
	}
	if !a.Bool("through") && !a.Has("depth") {
		return nil, fmt.Errorf("give `depth`, or through=true for a cut")
	}
	if a.Bool("through") && mode != "cut" {
		return nil, fmt.Errorf("through=true only applies to cuts")
	}
	p := map[string]any{"sketch": sk.ID, "mode": mode}
	copyOpt(p, a, "region", "target", "depth", "symmetric", "through", "reverse")
	id, err := uniqueID(env.Graph(), a, "extrude")
	if err != nil {
		return nil, err
	}
	deps := []string{sk.ID}
	if t := a.Str("target"); t != "" {
		deps = append(deps, t)
	}
	return addPlan(intentOr(a, fmt.Sprintf("Extrude sketch %s (%s)", sk.ID, mode)),
		fmt.Sprintf("Extrudes sketch %s (%s).", sk.ID, mode), newNode(env, a, id, "extrude", p, deps...)), nil
}
