package features

import (
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/selectors"
)

func init() {
	register(&Feature{Type: "hole", Build: toolBuild(holeTool), Tool: holeTool, Target: targetOf})
	register(&Feature{Type: "extrude", Build: toolBuild(extrudeTool), Tool: extrudeTool, Target: targetOf})
	register(&Feature{Type: "linear_pattern", Build: buildLinearPattern, Target: sourceOf})
	register(&Feature{Type: "circular_pattern", Build: buildCircularPattern, Target: sourceOf})
}

func sourceOf(p Params) string { return p.Str("source") }

// arena tracks intermediate shapes so every error path can release them.
type arena struct{ shapes []*api.Shape }

func (a *arena) add(s *api.Shape, err error) (*api.Shape, error) {
	if s != nil {
		a.shapes = append(a.shapes, s)
	}
	return s, err
}

// releaseExcept frees everything except the returned result.
func (a *arena) releaseExcept(keep *api.Shape) {
	for _, s := range a.shapes {
		if s != keep {
			s.Release()
		}
	}
	a.shapes = nil
}

// toolBuild turns a tool generator into a full builder: target ∘ tool.
func toolBuild(tool func(*Ctx) (*api.Shape, bool, error)) func(*Ctx) (*api.Shape, error) {
	return func(c *Ctx) (*api.Shape, error) {
		tl, subtract, err := tool(c)
		if err != nil {
			return nil, err
		}
		defer tl.Release()
		id := c.P.Str("target")
		if id == "" { // additive extrude without a target is just the tool body
			if subtract {
				return nil, fmt.Errorf("a cut needs a target")
			}
			return tl.Translate(0, 0, 0)
		}
		t, err := c.body(id)
		if err != nil {
			return nil, err
		}
		return applyTool(c.K, t.Shape, tl, subtract)
	}
}

// cylSpan is a cylinder along +Z from z0 to z1.
func cylSpan(a *arena, k *api.Kernel, r, z0, z1 float64) (*api.Shape, error) {
	cyl, err := a.add(k.Cylinder(r, z1-z0))
	if err != nil {
		return nil, err
	}
	return a.add(cyl.Translate(0, 0, z0))
}

// orient maps a tool authored along +Z (z=0 at the face, +Z into the material) onto a world
// face: tool +Z → -normal, tool origin → origin.
func orient(a *arena, tool *api.Shape, normal, origin [3]float64) (*api.Shape, error) {
	var rot *api.Shape
	var err error
	switch {
	case normal[2] < -0.5: // into material is +Z
		rot, err = a.add(tool.Translate(0, 0, 0))
	case normal[2] > 0.5: // into material is -Z
		rot, err = a.add(tool.Rotate(0, 0, 0, 1, 0, 0, math.Pi))
	case normal[0] < -0.5: // into +X
		rot, err = a.add(tool.Rotate(0, 0, 0, 0, 1, 0, math.Pi/2))
	case normal[0] > 0.5: // into -X
		rot, err = a.add(tool.Rotate(0, 0, 0, 0, 1, 0, -math.Pi/2))
	case normal[1] < -0.5: // into +Y
		rot, err = a.add(tool.Rotate(0, 0, 0, 1, 0, 0, -math.Pi/2))
	default: // into -Y
		rot, err = a.add(tool.Rotate(0, 0, 0, 1, 0, 0, math.Pi/2))
	}
	if err != nil {
		return nil, err
	}
	return a.add(rot.Translate(origin[0], origin[1], origin[2]))
}

// faceFor resolves the placement face: references.face, else +Z of the target.
func faceFor(c *Ctx) (selectors.Descriptor, *Body, error) {
	return faceOf(c, c.Node, c.P)
}

// faceOf is faceFor for an arbitrary node (a sketch placed on a face reads its own reference).
func faceOf(c *Ctx, n *cadir.Node, p Params) (selectors.Descriptor, *Body, error) {
	var sel selectors.Selector
	if raw := n.References["face"]; raw != "" {
		s, err := selectors.Parse(raw)
		if err != nil {
			return selectors.Descriptor{}, nil, err
		}
		sel = s
	} else {
		sel = selectors.Selector{Kind: "face", Args: map[string]string{"feature": p.Str("target"), "side": "+Z"}}
	}
	b, err := c.body(sel.Feature())
	if err != nil {
		return selectors.Descriptor{}, nil, fmt.Errorf("unresolved selector %s: %w", sel.String(), err)
	}
	d, err := selectors.ResolveTopo(sel, b.Topology())
	if err != nil {
		return d, nil, err
	}
	if d.Normal == nil {
		return d, nil, fmt.Errorf("selector %s does not select a face", sel.String())
	}
	return d, b, nil
}

func positionsOf(p Params, def [3]float64) ([][3]float64, error) {
	raw, ok := p["positions"]
	if !ok {
		return [][3]float64{def}, nil
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("positions must be a non-empty list of [x,y,z]")
	}
	out := make([][3]float64, 0, len(list))
	for i, it := range list {
		a, ok := it.([]any)
		if !ok || len(a) < 2 || len(a) > 3 {
			return nil, fmt.Errorf("positions[%d] must be [x,y] or [x,y,z]", i)
		}
		pt := def
		for j := range a {
			f, ok := a[j].(float64)
			if !ok {
				return nil, fmt.Errorf("positions[%d][%d] must be a number", i, j)
			}
			pt[j] = f
		}
		out = append(out, pt)
	}
	return out, nil
}

// holeCutter builds one hole in tool space: z=0 at the face, +Z into the material.
func holeCutter(a *arena, c *Ctx, kind string, dia, depth float64, through bool, thickness float64) (*api.Shape, error) {
	const lead = 1.0 // cutter starts this far outside the face to avoid coplanar faces
	end := depth
	if through {
		end = thickness + lead
	}
	cur, err := cylSpan(a, c.K, dia/2, -lead, end)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "simple":
		return cur, nil
	case "counterbore":
		cbd, err := c.P.Positive("cb_diameter")
		if err != nil {
			return nil, err
		}
		cbh, err := c.P.Positive("cb_depth")
		if err != nil {
			return nil, err
		}
		if cbd <= dia {
			return nil, fmt.Errorf("cb_diameter %g must exceed diameter %g", cbd, dia)
		}
		cb, err := cylSpan(a, c.K, cbd/2, -lead, cbh)
		if err != nil {
			return nil, err
		}
		return a.add(c.K.Union(cur, cb))
	case "countersink":
		csd, err := c.P.Positive("cs_diameter")
		if err != nil {
			return nil, err
		}
		ang, _ := c.P.NumOr("cs_angle_deg", 90)
		if csd <= dia || ang <= 0 || ang >= 180 {
			return nil, fmt.Errorf("countersink needs cs_diameter > diameter and 0 < cs_angle_deg < 180")
		}
		h := (csd - dia) / 2 / math.Tan(degToRad(ang)/2)
		cone, err := a.add(c.K.Cone(csd/2, dia/2, h)) // wide end at the face
		if err != nil {
			return nil, err
		}
		collar, err := cylSpan(a, c.K, csd/2, -lead, 0)
		if err != nil {
			return nil, err
		}
		u1, err := a.add(c.K.Union(cur, cone))
		if err != nil {
			return nil, err
		}
		return a.add(c.K.Union(u1, collar))
	}
	return nil, fmt.Errorf("kind must be simple, counterbore or countersink (got %q)", kind)
}

// holeTool builds all cutters of this node as one world-space tool.
// Positions are world coordinates; the component along the face normal comes from the face.
func holeTool(c *Ctx) (*api.Shape, bool, error) {
	dia, err := c.P.Positive("diameter")
	if err != nil {
		return nil, false, err
	}
	kind := c.P.Str("kind")
	if kind == "" {
		kind = "simple"
	}
	face, tb, err := faceFor(c)
	if err != nil {
		return nil, false, err
	}
	n := *face.Normal
	axis := 0
	for i := range n {
		if math.Abs(n[i]) > 0.5 {
			axis = i
		}
	}
	depth, _ := c.P.NumOr("depth", 0)
	through := depth <= 0
	pts, err := positionsOf(c.P, face.Centroid)
	if err != nil {
		return nil, false, err
	}

	a := &arena{}
	var all *api.Shape
	for _, pt := range pts {
		pt[axis] = face.Centroid[axis]
		for i := 0; i < 3; i++ {
			if i != axis && (pt[i] < face.BBox.Min[i]-1e-9 || pt[i] > face.BBox.Max[i]+1e-9) {
				a.releaseExcept(nil)
				return nil, false, fmt.Errorf("hole position (%g, %g, %g) lies outside the selected face", pt[0], pt[1], pt[2])
			}
		}
		cutter, err := holeCutter(a, c, kind, dia, depth, through, tb.Bounds().Size()[axis])
		if err == nil {
			cutter, err = orient(a, cutter, n, pt)
		}
		if err == nil {
			if all == nil {
				all = cutter
			} else {
				all, err = a.add(c.K.Union(all, cutter))
			}
		}
		if err != nil {
			a.releaseExcept(nil)
			return nil, false, err
		}
	}
	a.releaseExcept(all)
	return all, true, nil
}

// ---- extrude --------------------------------------------------------------

func extrudeTool(c *Ctx) (*api.Shape, bool, error) {
	mode := c.P.Str("mode")
	if mode == "" {
		mode = "add"
	}
	if mode != "add" && mode != "cut" {
		return nil, false, fmt.Errorf("mode must be add or cut (got %q)", mode)
	}
	plane := strings.ToUpper(c.P.Str("plane"))
	if plane == "" {
		plane = "XY"
	}
	offset, _ := c.P.NumOr("offset", 0)
	w0, depth := offset, 0.0
	if mode == "cut" && c.P.Bool("through") {
		t, err := c.body(c.P.Str("target"))
		if err != nil {
			return nil, false, err
		}
		b := t.Bounds()
		l := 10.0
		for i := 0; i < 3; i++ {
			l += 2 * math.Max(math.Abs(b.Min[i]), math.Abs(b.Max[i]))
			l += b.Max[i] - b.Min[i]
		}
		w0, depth = -l, 2*l
	} else {
		var err error
		if depth, err = c.P.Positive("depth"); err != nil {
			return nil, false, err
		}
		if c.P.Bool("symmetric") {
			w0 = offset - depth/2
		}
	}

	if sk := c.P.Str("sketch"); sk != "" {
		// A cut from a sketch placed on a face goes into the material unless reversed.
		reverse := c.P.Bool("reverse")
		if mode == "cut" {
			if n := c.NodeOf(sk); n != nil && n.References["face"] != "" {
				reverse = !reverse
			}
		}
		solid, err := sketchPrism(c, sk, c.P.Str("region"), w0, depth, reverse)
		if err != nil {
			return nil, false, err
		}
		return solid, mode == "cut", nil
	}

	a := &arena{}
	prism, err := profilePrism(a, c, depth)
	if err != nil {
		a.releaseExcept(nil)
		return nil, false, err
	}
	moved, err := a.add(prism.Translate(0, 0, w0))
	if err == nil {
		switch plane {
		case "XY":
		case "YZ":
			moved, err = a.add(moved.Rotate(0, 0, 0, 1, 1, 1, 2*math.Pi/3))
		case "XZ":
			moved, err = a.add(moved.Rotate(0, 0, 0, 1, 1, 1, 4*math.Pi/3))
		default:
			err = fmt.Errorf("plane must be XY, XZ or YZ (got %q)", plane)
		}
	}
	if err != nil {
		a.releaseExcept(nil)
		return nil, false, err
	}
	a.releaseExcept(moved)
	return moved, mode == "cut", nil
}

// profilePrism builds the sketch profile extruded from z=0 to z=depth in plane coordinates.
func profilePrism(a *arena, c *Ctx, depth float64) (*api.Shape, error) {
	switch c.P.Str("profile") {
	case "rectangle":
		w, err := c.P.Positive("w")
		if err != nil {
			return nil, err
		}
		h, err := c.P.Positive("h")
		if err != nil {
			return nil, err
		}
		x, _ := c.P.NumOr("x", 0)
		y, _ := c.P.NumOr("y", 0)
		box, err := a.add(c.K.Box(w, h, depth))
		if err != nil {
			return nil, err
		}
		return a.add(box.Translate(x, y, 0))
	case "circle":
		r, err := c.P.Positive("r")
		if err != nil {
			return nil, err
		}
		x, _ := c.P.NumOr("x", 0)
		y, _ := c.P.NumOr("y", 0)
		cyl, err := a.add(c.K.Cylinder(r, depth))
		if err != nil {
			return nil, err
		}
		return a.add(cyl.Translate(x, y, 0))
	case "slot":
		x1, err := c.P.Num("x1")
		if err != nil {
			return nil, err
		}
		y1, err := c.P.Num("y1")
		if err != nil {
			return nil, err
		}
		x2, err := c.P.Num("x2")
		if err != nil {
			return nil, err
		}
		y2, err := c.P.Num("y2")
		if err != nil {
			return nil, err
		}
		width, err := c.P.Positive("width")
		if err != nil {
			return nil, err
		}
		length := math.Hypot(x2-x1, y2-y1)
		if length < 1e-9 {
			return nil, fmt.Errorf("slot end points coincide")
		}
		r := width / 2
		body, err := a.add(c.K.Box(length, width, depth))
		if err != nil {
			return nil, err
		}
		body, err = a.add(body.Translate(0, -r, 0))
		if err != nil {
			return nil, err
		}
		cap1, err := a.add(c.K.Cylinder(r, depth))
		if err != nil {
			return nil, err
		}
		cap2, err := a.add(cap1.Translate(length, 0, 0))
		if err != nil {
			return nil, err
		}
		u, err := a.add(c.K.Union(body, cap1))
		if err != nil {
			return nil, err
		}
		u, err = a.add(c.K.Union(u, cap2))
		if err != nil {
			return nil, err
		}
		u, err = a.add(u.Rotate(0, 0, 0, 0, 0, 1, math.Atan2(y2-y1, x2-x1)))
		if err != nil {
			return nil, err
		}
		return a.add(u.Translate(x1, y1, 0))
	case "polygon":
		raw, ok := c.P["points"].([]any)
		if !ok || len(raw) < 3 {
			return nil, fmt.Errorf("polygon needs points: [[u,v], ...] with at least 3 points")
		}
		xy := make([]float64, 0, 2*len(raw))
		for i, it := range raw {
			pt, ok := it.([]any)
			if !ok || len(pt) != 2 {
				return nil, fmt.Errorf("points[%d] must be [u,v]", i)
			}
			for _, v := range pt {
				f, ok := v.(float64)
				if !ok {
					return nil, fmt.Errorf("points[%d] must be numeric", i)
				}
				xy = append(xy, f)
			}
		}
		return a.add(c.K.ExtrudePolygon(xy, depth))
	}
	return nil, fmt.Errorf("profile must be rectangle, circle, slot or polygon (got %q)", c.P.Str("profile"))
}

// ---- patterns -------------------------------------------------------------

// patternTools yields the source's tool shape and a function to apply a transformed copy.
func patternSource(c *Ctx) (src *Body, tool *api.Shape, subtract bool, err error) {
	id := c.P.Str("source")
	src, err = c.body(id)
	if err != nil {
		return nil, nil, false, err
	}
	sn := c.NodeOf(id)
	if sn == nil {
		return nil, nil, false, fmt.Errorf("source %q not found", id)
	}
	f, ok := Lookup(sn.Type)
	if !ok || f.Tool == nil {
		return nil, nil, false, fmt.Errorf("source %q is a %s; only hole and extrude features can be patterned", id, sn.Type)
	}
	sp, _ := c.ParamsOf(id)
	sub := *c
	sub.Node, sub.P = sn, sp
	tool, subtract, err = f.Tool(&sub)
	return src, tool, subtract, err
}

// applyCopies unites the copies of the tool and applies the result to src once. The copies
// are united pairwise, in rounds, rather than one after another: folding them into a growing
// accumulator makes every union work on all the geometry built so far, which is quadratic in
// the copy count (100 holes took 3x as long as 50 twice over), whereas a balanced reduction
// keeps each union small.
func applyCopies(c *Ctx, src *Body, tool *api.Shape, subtract bool, copies []func(a *arena, t *api.Shape) (*api.Shape, error)) (*api.Shape, error) {
	a := &arena{}
	parts := make([]*api.Shape, 0, len(copies))
	for _, mk := range copies {
		cp, err := mk(a, tool)
		if err != nil {
			a.releaseExcept(nil)
			return nil, err
		}
		parts = append(parts, cp)
	}
	acc, err := unionAll(c, a, parts)
	if err != nil {
		a.releaseExcept(nil)
		return nil, err
	}
	res, err := applyTool(c.K, src.Shape, acc, subtract)
	a.releaseExcept(nil)
	return res, err
}

// unionAll fuses shapes by balanced pairwise reduction; every intermediate is owned by a.
// It returns nil for an empty list.
func unionAll(c *Ctx, a *arena, parts []*api.Shape) (*api.Shape, error) {
	for len(parts) > 1 {
		next := make([]*api.Shape, 0, (len(parts)+1)/2)
		for i := 0; i+1 < len(parts); i += 2 {
			u, err := a.add(c.K.Union(parts[i], parts[i+1]))
			if err != nil {
				return nil, err
			}
			next = append(next, u)
		}
		if len(parts)%2 == 1 {
			next = append(next, parts[len(parts)-1])
		}
		parts = next
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return parts[0], nil
}

func buildLinearPattern(c *Ctx) (*api.Shape, error) {
	n, err := c.P.Num("count")
	if err != nil || n < 2 || n > 500 || n != math.Trunc(n) {
		return nil, fmt.Errorf("count must be an integer in [2, 500]")
	}
	dx, _ := c.P.NumOr("dx", 0)
	dy, _ := c.P.NumOr("dy", 0)
	dz, _ := c.P.NumOr("dz", 0)
	if dx == 0 && dy == 0 && dz == 0 {
		return nil, fmt.Errorf("pattern spacing (dx, dy, dz) must not be zero")
	}
	src, tool, subtract, err := patternSource(c)
	if err != nil {
		return nil, err
	}
	defer tool.Release()
	var copies []func(*arena, *api.Shape) (*api.Shape, error)
	for i := 1; i < int(n); i++ {
		i := float64(i)
		copies = append(copies, func(a *arena, t *api.Shape) (*api.Shape, error) {
			return a.add(t.Translate(dx*i, dy*i, dz*i))
		})
	}
	return applyCopies(c, src, tool, subtract, copies)
}

func buildCircularPattern(c *Ctx) (*api.Shape, error) {
	n, err := c.P.Num("count")
	if err != nil || n < 2 || n > 500 || n != math.Trunc(n) {
		return nil, fmt.Errorf("count must be an integer in [2, 500]")
	}
	total, _ := c.P.NumOr("total_angle_deg", 360)
	if total <= 0 || total > 360 {
		return nil, fmt.Errorf("total_angle_deg must be in (0, 360]")
	}
	cx, _ := c.P.NumOr("cx", 0)
	cy, _ := c.P.NumOr("cy", 0)
	cz, _ := c.P.NumOr("cz", 0)
	ax := [3]float64{0, 0, 1}
	switch strings.ToLower(c.P.Str("axis")) {
	case "", "z":
	case "x":
		ax = [3]float64{1, 0, 0}
	case "y":
		ax = [3]float64{0, 1, 0}
	default:
		return nil, fmt.Errorf("axis must be x, y or z")
	}
	step := total / n
	if total < 360 {
		step = total / (n - 1)
	}
	src, tool, subtract, err := patternSource(c)
	if err != nil {
		return nil, err
	}
	defer tool.Release()
	var copies []func(*arena, *api.Shape) (*api.Shape, error)
	for i := 1; i < int(n); i++ {
		ang := degToRad(step * float64(i))
		copies = append(copies, func(a *arena, t *api.Shape) (*api.Shape, error) {
			return a.add(t.Rotate(cx, cy, cz, ax[0], ax[1], ax[2], ang))
		})
	}
	return applyCopies(c, src, tool, subtract, copies)
}
