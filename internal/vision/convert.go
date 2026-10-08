package vision

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
)

// Sketch is a proposal converted to CAD-IR `sketch` node parameters, in millimetres.
type Sketch struct {
	Params      map[string]any
	Constraints []cadir.Constraint
	Warnings    []string
}

// DimParam is the sketch parameter name that carries a dimension's value.
func DimParam(id string) string { return "dim_" + strings.ToLower(id) }

func round3(v float64) float64 {
	v = math.Round(v*1000) / 1000
	if v == 0 {
		return 0 // no negative zero in stored geometry
	}
	return v
}

// node is one endpoint of a line or arc, shared with the neighbouring entity at a joint.
type node struct {
	ent string
	key string // p1 | p2
}

type snapper struct {
	pts    map[node]pt
	parent map[node]node
}

func newSnapper() *snapper { return &snapper{pts: map[node]pt{}, parent: map[node]node{}} }

func (s *snapper) find(n node) node {
	for s.parent[n] != n {
		s.parent[n] = s.parent[s.parent[n]]
		n = s.parent[n]
	}
	return n
}

func (s *snapper) add(n node, p pt) { s.pts[n] = p; s.parent[n] = n }

func (s *snapper) union(a, b node) {
	ra, rb := s.find(a), s.find(b)
	if ra != rb {
		s.parent[rb] = ra
	}
}

// groupPoint is the mean of a joint's points.
func (s *snapper) groupPoint(n node) pt {
	root := s.find(n)
	var sum pt
	var c float64
	for m, p := range s.pts {
		if s.find(m) == root {
			sum[0] += p[0]
			sum[1] += p[1]
			c++
		}
	}
	return pt{sum[0] / c, sum[1] / c}
}

func (s *snapper) setGroup(n node, axis int, v float64) {
	root := s.find(n)
	for m, p := range s.pts {
		if s.find(m) == root {
			p[axis] = v
			s.pts[m] = p
		}
	}
}

// ToSketch converts a validated proposal into sketch parameters. The scale must come from
// ResolveScale (a human-supplied dimension or an explicit scale); pixels alone never scale.
func ToSketch(p *Proposal, scale *Scale) (*Sketch, error) {
	if scale == nil || !(scale.MMPerPx > 0) {
		return nil, fmt.Errorf("no scale: give one real dimension or set the sketch scale first")
	}
	p = p.Clone()
	out := &Sketch{}
	warnf := func(f string, a ...any) { out.Warnings = append(out.Warnings, fmt.Sprintf(f, a...)) }

	// 1. close loops: chain open entities and weld their joints
	sn := newSnapper()
	used := map[string]bool{}
	loops := func(pr Profile) [][]string { return append([][]string{pr.Outer}, pr.Holes...) }
	for _, pr := range p.Profiles {
		for _, l := range loops(pr) {
			for _, id := range l {
				used[id] = true
			}
			if len(l) < 2 {
				continue
			}
			steps, _, _, err := chainLoop(p, l)
			if err != nil {
				return nil, fmt.Errorf("profile %q: %w", pr.ID, err)
			}
			ends := func(st chainStep) (node, node) {
				e := p.entity(st.ID)
				a, b := node{st.ID, "p1"}, node{st.ID, "p2"}
				for _, n := range []node{a, b} {
					if _, ok := sn.pts[n]; !ok {
						q, _ := e.pt(n.key)
						sn.add(n, q)
					}
				}
				if st.Reverse {
					return b, a
				}
				return a, b
			}
			for i, st := range steps {
				_, end := ends(st)
				next := steps[(i+1)%len(steps)]
				start, _ := ends(next)
				sn.union(end, start)
			}
		}
	}
	// weld: every joint becomes the mean of its members
	means := map[node]pt{}
	for n := range sn.pts {
		means[n] = sn.groupPoint(n)
	}
	for n, q := range means {
		sn.pts[n] = q
	}

	// 2. relationships that snap geometry
	for _, rel := range p.Relationships {
		if rel.Confidence > 0 && rel.Confidence < 0.5 {
			warnf("relationship %s %v ignored: confidence %.2f is below 0.5", rel.Kind, rel.Entities, rel.Confidence)
			continue
		}
		detail := "recorded"
		switch rel.Kind {
		case "concentric":
			if snapConcentric(p, rel.Entities) {
				detail = "snapped"
			}
		case "equal":
			if snapEqual(p, rel.Entities) {
				detail = "snapped"
			}
		case "horizontal", "vertical":
			axis := 1
			if rel.Kind == "vertical" {
				axis = 0
			}
			for _, id := range rel.Entities {
				e := p.entity(id)
				if e == nil || e.Kind() != "line" {
					continue
				}
				a, b := node{id, "p1"}, node{id, "p2"}
				if _, ok := sn.pts[a]; !ok {
					q1, _ := e.pt("p1")
					q2, _ := e.pt("p2")
					sn.add(a, q1)
					sn.add(b, q2)
				}
				m := (sn.groupPoint(a)[axis] + sn.groupPoint(b)[axis]) / 2
				sn.setGroup(a, axis, m)
				sn.setGroup(b, axis, m)
				detail = "snapped"
			}
		}
		out.Constraints = append(out.Constraints, cadir.Constraint{Kind: rel.Kind, Refs: append([]string(nil), rel.Entities...), Detail: detail})
	}
	for n, q := range sn.pts { // write welded endpoints back
		e := p.entity(n.ent)
		e[n.key] = []any{q[0], q[1]}
	}

	// 3. origin: bottom-left of the profiles' bounding box (image y grows downwards)
	var all []pt
	for _, pr := range p.Profiles {
		poly, _, err := loopOutline(p, pr.Outer)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", pr.ID, err)
		}
		all = append(all, poly...)
	}
	mn, mx := bboxOf(all)
	ox, oy := mn[0], mx[1]
	if p.Placement.Face != "" { // on a face the drawing is centred on the face centre
		ox, oy = (mn[0]+mx[0])/2, (mn[1]+mx[1])/2
	}
	s := scale.MMPerPx
	X := func(x float64) float64 { return round3((x - ox) * s) }
	Y := func(y float64) float64 { return round3((oy - y) * s) }
	P := func(q pt) []any { return []any{X(q[0]), Y(q[1])} }

	// 4. dimensions that drive geometry become parameters
	params := map[string]any{}
	drive := map[string]string{} // "entity.field" -> param reference
	for i := range p.Dimensions {
		d := &p.Dimensions[i]
		id, field, ok := d.bindParts()
		if !ok || !d.trusted() {
			continue
		}
		e := p.entity(id)
		if e == nil || !bindFields[e.Kind()][field] {
			continue
		}
		name := DimParam(d.ID)
		params[name] = round3(*d.Value)
		ref := "@" + name
		if field == "diameter" {
			ref += "/2"
			field = "radius"
		}
		drive[id+"."+field] = ref
		if px, ok := measurePx(e, field); ok && scale.Source == "explicit" {
			want := *d.Value
			if field == "radius" && strings.HasSuffix(ref, "/2") {
				want = *d.Value / 2
			}
			if math.Abs(want-px*s)/want > 0.05 {
				warnf("dimension %s (%.4g mm) differs from the drawn size scaled by the explicit scale (%.4g mm)", d.ID, *d.Value, px*s)
			}
		}
	}

	// 5. entities in millimetres
	var ents []any
	for _, e := range p.Entities {
		if !used[e.ID()] {
			warnf("entity %s is not part of any profile and was ignored", e.ID())
			continue
		}
		m := map[string]any{"id": e.ID(), "kind": e.Kind()}
		switch e.Kind() {
		case "line":
			a, _ := e.pt("p1")
			b, _ := e.pt("p2")
			m["p1"], m["p2"] = P(a), P(b)
		case "arc":
			a, _ := e.pt("p1")
			mid, _ := e.pt("mid")
			b, _ := e.pt("p2")
			m["p1"], m["mid"], m["p2"] = P(a), P(mid), P(b)
		case "circle":
			cx, _ := e.num("cx")
			cy, _ := e.num("cy")
			r, _ := e.num("r")
			m["cx"], m["cy"], m["r"] = X(cx), Y(cy), round3(r*s)
			if ref, ok := drive[e.ID()+".radius"]; ok {
				m["r"] = ref
			}
		case "rectangle":
			x, _ := e.num("x")
			y, _ := e.num("y")
			w, _ := e.num("w")
			h, _ := e.num("h")
			m["x"], m["y"], m["w"], m["h"] = X(x), Y(y+h), round3(w*s), round3(h*s)
			for _, f := range []string{"w", "h"} {
				if ref, ok := drive[e.ID()+"."+f]; ok {
					m[f] = ref
				}
			}
		case "polygon":
			raw, _ := e["points"].([]any)
			pts := make([]any, 0, len(raw))
			for _, it := range raw {
				a := it.([]any)
				pts = append(pts, P(pt{a[0].(float64), a[1].(float64)}))
			}
			m["points"] = pts
		case "slot":
			x1, _ := e.num("x1")
			y1, _ := e.num("y1")
			x2, _ := e.num("x2")
			y2, _ := e.num("y2")
			w, _ := e.num("width")
			m["x1"], m["y1"], m["x2"], m["y2"], m["width"] = X(x1), Y(y1), X(x2), Y(y2), round3(w*s)
			if ref, ok := drive[e.ID()+".width"]; ok {
				m["width"] = ref
			}
		}
		ents = append(ents, m)
	}
	regs := make([]any, 0, len(p.Profiles))
	for _, pr := range p.Profiles {
		r := map[string]any{"id": pr.ID, "outer": strs(pr.Outer)}
		if len(pr.Holes) > 0 {
			hs := make([]any, len(pr.Holes))
			for i, h := range pr.Holes {
				hs[i] = strs(h)
			}
			r["holes"] = hs
		}
		regs = append(regs, r)
	}
	params["entities"], params["regions"] = ents, regs
	if pl := p.Placement; pl.Face == "" {
		params["plane"] = firstNonEmpty(pl.Plane, "XY")
		if pl.Offset != 0 {
			params["offset"] = pl.Offset
		}
	} else if pl.Offset != 0 {
		params["offset"] = pl.Offset
	}
	// make the parameters plain decoded-JSON values (stable across save/load)
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	out.Params = map[string]any{}
	if err := json.Unmarshal(b, &out.Params); err != nil {
		return nil, err
	}
	if scale.Source == "dimension" {
		warnf("scale %.4g mm/px comes from the dimensions you confirmed; all other sizes are measured from the drawing with it", s)
	} else {
		warnf("scale %.4g mm/px was set explicitly; sizes are measured from the drawing with it", s)
	}
	return out, nil
}

func strs(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func snapConcentric(p *Proposal, ids []string) bool {
	var ref pt
	have := false
	for _, id := range ids {
		e := p.entity(id)
		if e == nil || e.Kind() != "circle" {
			continue
		}
		cx, _ := e.num("cx")
		cy, _ := e.num("cy")
		if !have {
			ref, have = pt{cx, cy}, true
			continue
		}
		e["cx"], e["cy"] = ref[0], ref[1]
	}
	return have
}

// snapEqual makes circles share a radius, slots a width and rectangles a size.
func snapEqual(p *Proposal, ids []string) bool {
	fields := map[string][]string{"circle": {"r"}, "slot": {"width"}, "rectangle": {"w", "h"}}
	snapped := false
	for kind, fs := range fields {
		for _, f := range fs {
			var group []Entity
			for _, id := range ids {
				if e := p.entity(id); e != nil && e.Kind() == kind {
					group = append(group, e)
				}
			}
			if len(group) < 2 {
				continue
			}
			var sum float64
			for _, e := range group {
				v, _ := e.num(f)
				sum += v
			}
			for _, e := range group {
				e[f] = sum / float64(len(group))
			}
			snapped = true
		}
	}
	return snapped
}
