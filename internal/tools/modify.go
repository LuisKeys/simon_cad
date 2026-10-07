package tools

import (
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/selectors"
)

// Body-level edits (scale, duplicate, align), face-level edits (extrude, inset, offset) and
// design-tree grouping.

// requireLeaf checks that id is a visible body: modifying a feature that a later feature
// already consumes would fork the chain into a second, overlapping body.
func requireLeaf(env Env, id string) (selectors.Bounds, error) {
	b, err := requireBounds(env, id)
	if err != nil {
		return b, err
	}
	m, err := env.Model()
	if err != nil {
		return b, err
	}
	for _, l := range m.Leaves {
		if l == id {
			return b, nil
		}
	}
	var users []string
	for _, d := range env.Graph().Dependents(id) {
		if n := env.Graph().Get(d); n != nil && !n.Suppressed && features.Consumes(n.Type) {
			users = append(users, d)
		}
	}
	if len(users) == 0 {
		return b, fmt.Errorf("feature %q is not a visible body", id)
	}
	return b, fmt.Errorf("feature %q is not a visible body: %s builds on it, so apply this to %q instead", id, quoteList(users), users[len(users)-1])
}

// targetIDs reads `features` (or, when omitted, the bodies of the current selection), in
// order and de-duplicated.
func targetIDs(env Env, a Args) ([]string, error) {
	var raw []string
	if list, ok := a["features"].([]any); ok {
		for _, v := range list {
			s, _ := v.(string)
			raw = append(raw, s)
		}
	} else {
		for _, ref := range env.SelectionRefs() {
			raw = append(raw, selectors.FeatureOf(ref))
		}
	}
	var out []string
	for _, id := range raw {
		if id != "" && !contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no target: pass `features` or select objects first")
	}
	for _, id := range out {
		if _, err := requireFeature(env, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// freshID is uniqueID that also avoids ids already taken by earlier nodes of the same plan.
func freshID(env Env, taken map[string]bool, base string) string {
	g := env.Graph()
	if len(base) > 40 {
		base = base[:40]
	}
	if g.Get(base) == nil && !taken[base] {
		taken[base] = true
		return base
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s_%d", base, i)
		if g.Get(id) == nil && !taken[id] {
			taken[id] = true
			return id
		}
	}
}

// planFace parses and resolves the `face` selector of a face tool; its feature must be a
// visible body. planar=true also requires a planar face.
func planFace(env Env, a Args, planar bool) (selectors.Selector, selectors.Descriptor, error) {
	sel, err := selectors.Parse(a.Str("face"))
	if err != nil {
		return sel, selectors.Descriptor{}, err
	}
	if sel.Kind != "face" {
		return sel, selectors.Descriptor{}, fmt.Errorf("face must be a face(...) selector, got %s", sel.Kind)
	}
	if _, err := requireLeaf(env, sel.Feature()); err != nil {
		return sel, selectors.Descriptor{}, err
	}
	d, err := resolveSelector(env, sel)
	if err != nil {
		return sel, d, err
	}
	if planar && d.Normal == nil {
		return sel, d, fmt.Errorf("%s is a %s face; only planar faces can be extruded or inset (face_offset works on any face)", sel.String(), d.SurfaceType)
	}
	return sel, d, nil
}

func faceNode(env Env, a Args, sel selectors.Selector, typ string, params map[string]any) (*cadir.Node, error) {
	id, err := uniqueID(env.Graph(), a, typ)
	if err != nil {
		return nil, err
	}
	params["target"] = sel.Feature()
	n := newNode(env, a, id, typ, params, sel.Feature())
	n.References = map[string]string{"face": sel.String()}
	n.SelectionRefs = appendUnique(n.SelectionRefs, sel.String())
	return n, nil
}

func signedNonZero(a Args, name string) (float64, error) {
	v := a.Num(name)
	if math.Abs(v) < 1e-6 {
		return 0, fmt.Errorf("%s must be non-zero: positive adds material (outward), negative removes it (inward)", name)
	}
	return v, nil
}

var faceProp = text("Face selector, e.g. face(feature=plate,side=+Z) or face(feature=plate,index=4) from the selection")

func modifyTools() []*Tool {
	return []*Tool{
		{
			Name: "scale",
			Description: "Scale a body. Give `factor` for uniform scaling (2 = twice as big, 0.5 = half) or per-axis sx/sy/sz (each defaults to `factor`, else 1). " +
				"`about` is the fixed point: center (default, the bounding-box center), base (center of the bottom face, keeps a part on the print bed), origin, or point (ox,oy,oz). " +
				"Non-uniform scaling turns cylinders and other analytic faces into free-form surfaces.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"target": text("Visible body to scale"),
				"factor": positive("Uniform scale factor"),
				"sx":     positive("Scale factor along X"), "sy": positive("Scale factor along Y"), "sz": positive("Scale factor along Z"),
				"about": enum("Fixed point of the scaling", features.ScaleAnchors...),
				"ox":    number("Fixed point X (about=point)"), "oy": number("Fixed point Y (about=point)"), "oz": number("Fixed point Z (about=point)"),
			}), "target"),
			Plan: func(env Env, a Args) (*Plan, error) {
				if _, err := requireLeaf(env, a.Str("target")); err != nil {
					return nil, err
				}
				if !a.Has("factor") && !a.Has("sx") && !a.Has("sy") && !a.Has("sz") {
					return nil, fmt.Errorf("give `factor` (uniform) or sx/sy/sz")
				}
				f := a.NumOr("factor", 1)
				desc := []string{}
				for _, k := range []string{"sx", "sy", "sz"} {
					v := a.NumOr(k, f)
					if v > 1000 {
						return nil, fmt.Errorf("%s=%s is too large (at most 1000)", k, fmtMM(v))
					}
					desc = append(desc, fmtMM(v))
				}
				if desc[0] == "1" && desc[1] == "1" && desc[2] == "1" {
					return nil, fmt.Errorf("the scale factors are all 1, so nothing would change")
				}
				if (a.Has("ox") || a.Has("oy") || a.Has("oz")) && a.Str("about") != "point" {
					return nil, fmt.Errorf("ox/oy/oz need about=point")
				}
				id, err := uniqueID(env.Graph(), a, "scale")
				if err != nil {
					return nil, err
				}
				p := map[string]any{"target": a.Str("target")}
				copyOpt(p, a, "factor", "sx", "sy", "sz", "about", "ox", "oy", "oz")
				what := "x" + desc[0]
				if desc[0] != desc[1] || desc[1] != desc[2] {
					what = "x" + strings.Join(desc, " / ")
				}
				return addPlan(intentOr(a, "Scale "+a.Str("target")+" "+what),
					fmt.Sprintf("Scales %q %s about its %s.", a.Str("target"), what, orDefault(a.Str("about"), "center")),
					newNode(env, a, id, "scale", p, a.Str("target"))), nil
			},
		},
		{
			Name: "duplicate",
			Description: "Duplicate bodies: each copy is a linked copy of its source moved by (dx,dy,dz) mm. The source stays as it is; the copy follows later parameter edits of the source. " +
				"Pass `features` (several ids → one copy each, in one change) or omit it to copy the current selection. The copy keeps the source's appearance and group.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"features": array("Feature ids to duplicate; omit to use the current selection", text("Feature id"), 1, 64),
				"dx":       number("Offset X (mm)"), "dy": number("Offset Y (mm)"), "dz": number("Offset Z (mm)"),
			})),
			Plan: func(env Env, a Args) (*Plan, error) {
				ids, err := targetIDs(env, a)
				if err != nil {
					return nil, err
				}
				if len(ids) > 1 && (a.Str("id") != "" || a.Str("name") != "") {
					return nil, fmt.Errorf("id and name only apply when duplicating one feature")
				}
				p := &Plan{LowRisk: true}
				taken := map[string]bool{}
				var made []string
				for _, src := range ids {
					if _, err := requireBounds(env, src); err != nil {
						return nil, err
					}
					var id string
					if len(ids) == 1 && (a.Str("id") != "" || a.Str("name") != "") {
						if id, err = uniqueID(env.Graph(), a, "copy"); err != nil {
							return nil, err
						}
					} else {
						id = freshID(env, taken, src+"_copy")
					}
					params := map[string]any{"source": src}
					copyOpt(params, a, "dx", "dy", "dz")
					n := newNode(env, a, id, "copy", params, src)
					if a.Str("name") == "" {
						n.Metadata.Name = env.Graph().Get(src).Metadata.Name + " copy"
						if env.Graph().Get(src).Metadata.Name == "" {
							n.Metadata.Name = src + " copy"
						}
					}
					p.Commands = append(p.Commands, &commands.AddFeature{Node: n, Index: -1})
					made = append(made, id)
				}
				off := fmt.Sprintf("(%s, %s, %s) mm", fmtMM(a.NumOr("dx", 0)), fmtMM(a.NumOr("dy", 0)), fmtMM(a.NumOr("dz", 0)))
				p.Intent = intentOr(a, "Duplicate "+quoteList(ids))
				p.Explanation = fmt.Sprintf("Adds linked copies %s of %s, offset by %s.", quoteList(made), quoteList(ids), off)
				return p, nil
			},
		},
		{
			Name: "align",
			Description: "Align bodies along one or more axes by moving them (each moved body gets a transform feature). mode min/center/max aligns the bodies' bounding-box minimum, center or maximum. " +
				"to=first (default with 2+ bodies): to the first listed body, which stays put; to=selection: to the bounds of all listed bodies; to=origin (default with 1 body): to the world plane through 0 " +
				"(e.g. axes=[z], mode=min puts a part on the floor; axes=[x,y], mode=center centers it). `reference` names a body that stays put instead. Omit `features` to use the current selection.",
			Capability: capGeometry,
			Schema: object(props{
				"features":  array("Bodies to align; omit to use the current selection", text("Feature id"), 1, 64),
				"axes":      array("Axes to align along", enum("Axis", "x", "y", "z"), 1, 3),
				"mode":      enum("Which side of the bounding boxes to align", "min", "center", "max"),
				"to":        enum("What to align to", "first", "selection", "origin"),
				"reference": text("Optional body that stays put and is aligned to"),
				"intent":    text("Why, in natural language"),
			}, "axes"),
			Plan: planAlign,
		},
		{
			Name: "face_extrude",
			Description: "Push/pull a planar face: extrude its exact outline along its outward normal. distance > 0 adds material outward, distance < 0 cuts into the body. " +
				"Use the selected face's selector as `face`.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"face": faceProp, "distance": number("Signed distance (mm): + outward (adds), - inward (cuts)"),
			}), "face", "distance"),
			Plan: func(env Env, a Args) (*Plan, error) {
				dist, err := signedNonZero(a, "distance")
				if err != nil {
					return nil, err
				}
				sel, _, err := planFace(env, a, true)
				if err != nil {
					return nil, err
				}
				n, err := faceNode(env, a, sel, "face_extrude", map[string]any{"distance": dist})
				if err != nil {
					return nil, err
				}
				verb := "Extrudes"
				if dist < 0 {
					verb = "Cuts"
				}
				return addPlan(intentOr(a, fmt.Sprintf("Extrude face %s by %s mm", sel.String(), fmtMM(dist))),
					fmt.Sprintf("%s %s by %s mm.", verb, sel.String(), fmtMM(math.Abs(dist))), n), nil
			},
		},
		{
			Name: "face_inset",
			Description: "Inset a planar face: shrink its outline by `inset` mm from every edge. With `depth` the inset region is raised (depth > 0, a pad) or sunk (depth < 0, a pocket leaving a rim). " +
				"Without depth the inset outline is imprinted, splitting the face so the inner face can be selected and extruded on its own.",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"face": faceProp, "inset": positive("Distance from the face's edges (mm)"),
				"depth": number("Signed depth (mm): + raises a pad, - sinks a pocket; omit to only split the face"),
			}), "face", "inset"),
			Plan: func(env Env, a Args) (*Plan, error) {
				sel, d, err := planFace(env, a, true)
				if err != nil {
					return nil, err
				}
				in := a.Num("inset")
				// The inset must leave something of the face: it cannot exceed half the narrowest extent.
				sz := d.BBox.Size()
				narrow := math.Inf(1)
				for i := 0; i < 3; i++ {
					if math.Abs((*d.Normal)[i]) < 0.9 && sz[i] > 1e-9 {
						narrow = math.Min(narrow, sz[i])
					}
				}
				if axisAligned(*d.Normal) && in*2 >= narrow {
					return nil, fmt.Errorf("inset %s mm leaves nothing of the face (%s mm across): use less than %s mm", fmtMM(in), fmtMM(narrow), fmtMM(narrow/2))
				}
				params := map[string]any{"inset": in}
				what := "imprints the inset outline (splits the face)"
				if a.Has("depth") {
					dep, err := signedNonZero(a, "depth")
					if err != nil {
						return nil, err
					}
					params["depth"] = dep
					what = fmt.Sprintf("raises the inset region %s mm", fmtMM(dep))
					if dep < 0 {
						what = fmt.Sprintf("sinks the inset region %s mm", fmtMM(-dep))
					}
				}
				n, err := faceNode(env, a, sel, "face_inset", params)
				if err != nil {
					return nil, err
				}
				return addPlan(intentOr(a, fmt.Sprintf("Inset face %s by %s mm", sel.String(), fmtMM(in))),
					fmt.Sprintf("Insets %s by %s mm and %s.", sel.String(), fmtMM(in), what), n), nil
			},
		},
		{
			Name: "face_offset",
			Description: "Offset a face: move it along its normal by `distance` mm, extending or trimming the faces around it. Works on any face: a planar face moves (like push/pull), " +
				"a cylindrical face changes radius (positive = outward: a hole gets smaller, a boss bigger).",
			Capability: capGeometry,
			Schema: object(withMeta(props{
				"face": faceProp, "distance": number("Signed distance (mm): + outward, - inward"),
			}), "face", "distance"),
			Plan: func(env Env, a Args) (*Plan, error) {
				dist, err := signedNonZero(a, "distance")
				if err != nil {
					return nil, err
				}
				sel, _, err := planFace(env, a, false)
				if err != nil {
					return nil, err
				}
				n, err := faceNode(env, a, sel, "face_offset", map[string]any{"distance": dist})
				if err != nil {
					return nil, err
				}
				return addPlan(intentOr(a, fmt.Sprintf("Offset face %s by %s mm", sel.String(), fmtMM(dist))),
					fmt.Sprintf("Offsets %s by %s mm.", sel.String(), fmtMM(dist)), n), nil
			},
		},
		{
			Name: "group",
			Description: "Put features/bodies into a named group in the design tree (organization only; geometry is not rebuilt). Omit `features` to group the current selection. " +
				"Grouping features that are already in a group moves them; reusing an existing group name adds to that group (also how a group is renamed: regroup all its members under the new name).",
			Capability: capGeometry,
			Schema: object(props{
				"features": featuresProp, "name": text("Group name"), "intent": text("Why"),
			}, "name"),
			Plan: func(env Env, a Args) (*Plan, error) {
				name, err := cadir.NormalizeGroup(a.Str("name"))
				if err != nil {
					return nil, err
				}
				if name == "" {
					return nil, fmt.Errorf("give the group a name")
				}
				ids, err := targetIDs(env, a)
				if err != nil {
					return nil, err
				}
				p := &Plan{Intent: intentOr(a, "Group "+quoteList(ids)+" as "+name),
					Explanation: fmt.Sprintf("Puts %s in group %q. Organization only; geometry is not rebuilt.", quoteList(ids), name)}
				for _, id := range ids {
					if env.Graph().Get(id).Group != name {
						p.Commands = append(p.Commands, &commands.SetGroup{ID: id, Group: name})
					}
				}
				if len(p.Commands) == 0 {
					return nil, fmt.Errorf("nothing to change: already as requested")
				}
				return p, nil
			},
		},
		{
			Name:        "ungroup",
			Description: "Remove features from their group (organization only). Give `group` to dissolve a whole group, or `features`; omit both to ungroup the current selection.",
			Capability:  capGeometry,
			Schema: object(props{
				"features": featuresProp, "group": text("Name of a group to dissolve"), "intent": text("Why"),
			}),
			Plan: func(env Env, a Args) (*Plan, error) {
				var ids []string
				if g := strings.TrimSpace(a.Str("group")); g != "" {
					for _, n := range env.Graph().Nodes {
						if n.Group == g {
							ids = append(ids, n.ID)
						}
					}
					if len(ids) == 0 {
						return nil, fmt.Errorf("there is no group %q (groups: %s)", g, strings.Join(Groups(env.Graph()), ", "))
					}
				} else {
					var err error
					if ids, err = targetIDs(env, a); err != nil {
						return nil, err
					}
				}
				p := &Plan{Intent: intentOr(a, "Ungroup "+quoteList(ids)),
					Explanation: fmt.Sprintf("Removes %s from their group. Organization only.", quoteList(ids))}
				for _, id := range ids {
					if env.Graph().Get(id).Group != "" {
						p.Commands = append(p.Commands, &commands.SetGroup{ID: id})
					}
				}
				if len(p.Commands) == 0 {
					return nil, fmt.Errorf("nothing to change: %s not in a group", quoteList(ids))
				}
				return p, nil
			},
		},
	}
}

// Groups lists the group names of a graph in first-appearance order.
func Groups(g *cadir.Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Group != "" && !contains(out, n.Group) {
			out = append(out, n.Group)
		}
	}
	return out
}

func axisAligned(n [3]float64) bool {
	for _, v := range n {
		if math.Abs(math.Abs(v)-1) < 1e-6 {
			return true
		}
	}
	return false
}

func planAlign(env Env, a Args) (*Plan, error) {
	ids, err := targetIDs(env, a)
	if err != nil {
		return nil, err
	}
	bounds := map[string]selectors.Bounds{}
	for _, id := range ids {
		b, err := requireLeaf(env, id)
		if err != nil {
			return nil, err
		}
		bounds[id] = b
	}
	ref, to := a.Str("reference"), a.Str("to")
	if ref != "" && to != "" {
		return nil, fmt.Errorf("give either `reference` or `to`, not both")
	}
	if ref == "" && to == "" {
		to = "first"
		if len(ids) == 1 {
			to = "origin"
		}
	}
	var target selectors.Bounds
	moving := ids
	switch {
	case ref != "":
		b, err := requireBounds(env, ref)
		if err != nil {
			return nil, err
		}
		target, moving = b, without(ids, ref)
	case to == "first":
		if len(ids) < 2 {
			return nil, fmt.Errorf("to=first needs at least two bodies (the first one stays put); use to=origin for one body")
		}
		target, moving = bounds[ids[0]], ids[1:]
	case to == "selection":
		if len(ids) < 2 {
			return nil, fmt.Errorf("to=selection needs at least two bodies")
		}
		target = bounds[ids[0]]
		for _, id := range ids[1:] {
			b := bounds[id]
			for i := 0; i < 3; i++ {
				target.Min[i] = math.Min(target.Min[i], b.Min[i])
				target.Max[i] = math.Max(target.Max[i], b.Max[i])
			}
		}
	case to == "origin":
		// zero-size bounds at the origin: min, center and max are all 0
	}
	if len(moving) == 0 {
		return nil, fmt.Errorf("nothing to move: the reference is the only body")
	}
	mode := orDefault(a.Str("mode"), "center")
	pick := func(b selectors.Bounds, i int) float64 {
		switch mode {
		case "min":
			return b.Min[i]
		case "max":
			return b.Max[i]
		}
		return (b.Min[i] + b.Max[i]) / 2
	}
	var axes []int
	var axisNames []string
	for _, v := range a["axes"].([]any) {
		i := strings.Index("xyz", v.(string))
		if !containsInt(axes, i) {
			axes = append(axes, i)
			axisNames = append(axisNames, strings.ToUpper(v.(string)))
		}
	}
	refName := map[string]string{"first": quoteList(ids[:1]), "selection": "the selection bounds", "origin": "the origin"}[to]
	if ref != "" {
		refName = quoteList([]string{ref})
	}
	p := &Plan{LowRisk: true,
		Intent: intentOr(a, fmt.Sprintf("Align %s (%s %s) to %s", quoteList(moving), mode, strings.Join(axisNames, "/"), refName))}
	var moved []string
	for _, id := range moving {
		var delta [3]float64
		for _, i := range axes {
			delta[i] = pick(target, i) - pick(bounds[id], i)
			if math.Abs(delta[i]) < 1e-9 {
				delta[i] = 0
			}
		}
		if delta == [3]float64{} {
			continue
		}
		params := map[string]any{"target": id}
		for i, k := range []string{"dx", "dy", "dz"} {
			if delta[i] != 0 {
				params[k] = math.Round(delta[i]*1e6) / 1e6
			}
		}
		nid, err := uniqueID(env.Graph(), Args{}, "align_"+id)
		if err != nil {
			return nil, err
		}
		n := newNode(env, Args{"intent": p.Intent}, nid, "transform", params, id)
		p.Commands = append(p.Commands, &commands.AddFeature{Node: n, Index: -1})
		moved = append(moved, id)
	}
	if len(moved) == 0 {
		return nil, fmt.Errorf("nothing to change: already aligned")
	}
	p.Explanation = fmt.Sprintf("Moves %s so their %s %s match %s.", quoteList(moved), mode, strings.Join(axisNames, "/"), refName)
	return p, nil
}

func without(list []string, s string) []string {
	var out []string
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
