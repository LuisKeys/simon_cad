package features

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/api"
)

// The sketch node is planar 2D geometry. Parameters:
//
//	plane    XY | XZ | YZ (default XY); offset moves it along the plane normal
//	         (or references.face: a planar face, origin at its centroid, u along its longest straight edge)
//	entities [{id, kind, ...}] in mm, kind ∈ line{p1,p2} arc{p1,mid,p2} circle{cx,cy,r}
//	         rectangle{x,y,w,h} polygon{points} slot{x1,y1,x2,y2,width}
//	regions  [{id, outer:[entity ids], holes:[[entity ids]]}]; omitted → one region per closed entity
//
// Any number inside an entity may be "@name" (or "@name/2" for half of it): the node's own
// numeric parameter "name", so dimensions stay editable after commit.
func init() {
	register(&Feature{Type: "sketch", Kind: KindSketch, Build: buildSketch})
}

const sketchTol = 1e-6

type sketchEntity struct {
	segs   []api.Seg2D
	closed bool // the entity is a loop on its own
}

type sketchRegion struct {
	ID    string
	Outer []string
	Holes [][]string
}

// jsonRoundTrip normalises Go-built values ([]map[string]any, ints...) into decoded-JSON form.
func jsonRoundTrip(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// sketchNum resolves a number or an "@param" reference.
func sketchNum(p Params, v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, fmt.Errorf("non-finite number")
		}
		return t, nil
	case string:
		if strings.HasPrefix(t, "@") {
			name, half := t[1:], false
			if strings.HasSuffix(name, "/2") { // "@dim_d/2": half of a diameter parameter
				name, half = strings.TrimSuffix(name, "/2"), true
			}
			f, ok := p[name].(float64)
			if !ok {
				return 0, fmt.Errorf("%s: no numeric parameter %q on the sketch", t, name)
			}
			if half {
				f /= 2
			}
			return f, nil
		}
	}
	return 0, fmt.Errorf("expected a number or \"@param\", got %v", v)
}

func sketchPt(p Params, v any) ([2]float64, error) {
	a, ok := v.([]any)
	if !ok || len(a) != 2 {
		return [2]float64{}, fmt.Errorf("point must be [u,v]")
	}
	u, err := sketchNum(p, a[0])
	if err != nil {
		return [2]float64{}, err
	}
	w, err := sketchNum(p, a[1])
	if err != nil {
		return [2]float64{}, err
	}
	return [2]float64{u, w}, nil
}

func bezierSeg(a, c1, c2, b [2]float64) api.Seg2D {
	return api.Seg2D{Kind: 3, A: [8]float64{a[0], a[1], c1[0], c1[1], c2[0], c2[1], b[0], b[1]}}
}

func lineSeg(a, b [2]float64) api.Seg2D {
	return api.Seg2D{Kind: 0, A: [8]float64{a[0], a[1], b[0], b[1]}}
}

func entityOf(p Params, m map[string]any) (sketchEntity, error) {
	nums := func(keys ...string) ([]float64, error) {
		out := make([]float64, len(keys))
		for i, k := range keys {
			v, ok := m[k]
			if !ok {
				return nil, fmt.Errorf("missing %q", k)
			}
			f, err := sketchNum(p, v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[i] = f
		}
		return out, nil
	}
	pt := func(k string) ([2]float64, error) {
		v, ok := m[k]
		if !ok {
			return [2]float64{}, fmt.Errorf("missing %q", k)
		}
		r, err := sketchPt(p, v)
		if err != nil {
			return r, fmt.Errorf("%s: %w", k, err)
		}
		return r, nil
	}
	kind, _ := m["kind"].(string)
	switch kind {
	case "line":
		a, err := pt("p1")
		if err != nil {
			return sketchEntity{}, err
		}
		b, err := pt("p2")
		if err != nil {
			return sketchEntity{}, err
		}
		if math.Hypot(a[0]-b[0], a[1]-b[1]) < sketchTol {
			return sketchEntity{}, fmt.Errorf("line endpoints coincide")
		}
		return sketchEntity{segs: []api.Seg2D{lineSeg(a, b)}}, nil
	case "arc":
		a, err := pt("p1")
		if err != nil {
			return sketchEntity{}, err
		}
		mid, err := pt("mid")
		if err != nil {
			return sketchEntity{}, err
		}
		b, err := pt("p2")
		if err != nil {
			return sketchEntity{}, err
		}
		return sketchEntity{segs: []api.Seg2D{{Kind: 1, A: [8]float64{a[0], a[1], mid[0], mid[1], b[0], b[1]}}}}, nil
	case "circle":
		v, err := nums("cx", "cy", "r")
		if err != nil {
			return sketchEntity{}, err
		}
		if !(v[2] > 0) {
			return sketchEntity{}, fmt.Errorf("circle r must be > 0")
		}
		return sketchEntity{segs: []api.Seg2D{{Kind: 2, A: [8]float64{v[0], v[1], v[2]}}}, closed: true}, nil
	case "rectangle":
		v, err := nums("x", "y", "w", "h")
		if err != nil {
			return sketchEntity{}, err
		}
		if !(v[2] > 0 && v[3] > 0) {
			return sketchEntity{}, fmt.Errorf("rectangle w and h must be > 0")
		}
		x, y, w, h := v[0], v[1], v[2], v[3]
		return sketchEntity{segs: []api.Seg2D{
			lineSeg([2]float64{x, y}, [2]float64{x + w, y}),
			lineSeg([2]float64{x + w, y}, [2]float64{x + w, y + h}),
			lineSeg([2]float64{x + w, y + h}, [2]float64{x, y + h}),
			lineSeg([2]float64{x, y + h}, [2]float64{x, y}),
		}, closed: true}, nil
	case "polygon":
		raw, ok := m["points"].([]any)
		if !ok || len(raw) < 3 {
			return sketchEntity{}, fmt.Errorf("polygon needs points: [[u,v], ...] with at least 3 points")
		}
		pts := make([][2]float64, len(raw))
		for i, it := range raw {
			r, err := sketchPt(p, it)
			if err != nil {
				return sketchEntity{}, fmt.Errorf("points[%d]: %w", i, err)
			}
			pts[i] = r
		}
		segs := make([]api.Seg2D, len(pts))
		for i := range pts {
			segs[i] = lineSeg(pts[i], pts[(i+1)%len(pts)])
		}
		return sketchEntity{segs: segs, closed: true}, nil
	case "bezier":
		a, err := pt("p1")
		if err != nil {
			return sketchEntity{}, err
		}
		c1, err := pt("c1")
		if err != nil {
			return sketchEntity{}, err
		}
		c2, err := pt("c2")
		if err != nil {
			return sketchEntity{}, err
		}
		b, err := pt("p2")
		if err != nil {
			return sketchEntity{}, err
		}
		if math.Hypot(a[0]-b[0], a[1]-b[1]) < sketchTol && math.Hypot(a[0]-c1[0], a[1]-c1[1]) < sketchTol {
			return sketchEntity{}, fmt.Errorf("bezier endpoints coincide")
		}
		return sketchEntity{segs: []api.Seg2D{bezierSeg(a, c1, c2, b)}}, nil
	case "curve":
		// a closed loop of cubic bezier segments: segments: [[p1,c1,c2,p2], ...] where every
		// segment starts where the previous one ended (the last one ends at the first start)
		raw, ok := m["segments"].([]any)
		if !ok || len(raw) < 1 {
			return sketchEntity{}, fmt.Errorf("curve needs segments: [[p1,c1,c2,p2], ...]")
		}
		segs := make([]api.Seg2D, len(raw))
		for i, it := range raw {
			quad, ok := it.([]any)
			if !ok || len(quad) != 4 {
				return sketchEntity{}, fmt.Errorf("segments[%d] must be [p1, c1, c2, p2]", i)
			}
			var q [4][2]float64
			for k := range quad {
				r, err := sketchPt(p, quad[k])
				if err != nil {
					return sketchEntity{}, fmt.Errorf("segments[%d][%d]: %w", i, k, err)
				}
				q[k] = r
			}
			segs[i] = bezierSeg(q[0], q[1], q[2], q[3])
		}
		for i := range segs {
			if !near(segEnd(segs[i]), segStart(segs[(i+1)%len(segs)])) {
				return sketchEntity{}, fmt.Errorf("curve segment %d does not end where segment %d starts", i, (i+1)%len(segs))
			}
		}
		return sketchEntity{segs: segs, closed: true}, nil
	case "slot":
		v, err := nums("x1", "y1", "x2", "y2", "width")
		if err != nil {
			return sketchEntity{}, err
		}
		if !(v[4] > 0) {
			return sketchEntity{}, fmt.Errorf("slot width must be > 0")
		}
		dx, dy := v[2]-v[0], v[3]-v[1]
		l := math.Hypot(dx, dy)
		if l < sketchTol {
			return sketchEntity{}, fmt.Errorf("slot end points coincide")
		}
		r := v[4] / 2
		ux, uy := dx/l, dy/l // along the slot
		nx, ny := -uy, ux    // left normal
		a1 := [2]float64{v[0] + nx*r, v[1] + ny*r}
		a2 := [2]float64{v[2] + nx*r, v[3] + ny*r}
		b2 := [2]float64{v[2] - nx*r, v[3] - ny*r}
		b1 := [2]float64{v[0] - nx*r, v[1] - ny*r}
		cap2 := [2]float64{v[2] + ux*r, v[3] + uy*r}
		cap1 := [2]float64{v[0] - ux*r, v[1] - uy*r}
		return sketchEntity{segs: []api.Seg2D{
			lineSeg(a1, a2),
			{Kind: 1, A: [8]float64{a2[0], a2[1], cap2[0], cap2[1], b2[0], b2[1]}},
			lineSeg(b2, b1),
			{Kind: 1, A: [8]float64{b1[0], b1[1], cap1[0], cap1[1], a1[0], a1[1]}},
		}, closed: true}, nil
	}
	return sketchEntity{}, fmt.Errorf("entity kind must be line, arc, bezier, curve, circle, rectangle, polygon or slot (got %q)", kind)
}

// sketchSpec is a sketch node's parameters resolved into segments and regions.
type sketchSpec struct {
	ents    map[string]sketchEntity
	order   []string
	regions []sketchRegion
}

func parseSketch(p Params) (*sketchSpec, error) {
	var ents []map[string]any
	if err := jsonRoundTrip(p["entities"], &ents); err != nil || len(ents) == 0 {
		return nil, fmt.Errorf("sketch needs entities: [{id, kind, ...}]")
	}
	sp := &sketchSpec{ents: map[string]sketchEntity{}}
	for i, m := range ents {
		id, _ := m["id"].(string)
		if id == "" {
			return nil, fmt.Errorf("entities[%d] needs an id", i)
		}
		if _, dup := sp.ents[id]; dup {
			return nil, fmt.Errorf("duplicate entity id %q", id)
		}
		e, err := entityOf(p, m)
		if err != nil {
			return nil, fmt.Errorf("entity %q: %w", id, err)
		}
		sp.ents[id] = e
		sp.order = append(sp.order, id)
	}
	if raw, ok := p["regions"]; ok && raw != nil {
		var regs []sketchRegion
		var in []struct {
			ID    string     `json:"id"`
			Outer []string   `json:"outer"`
			Holes [][]string `json:"holes"`
		}
		if err := jsonRoundTrip(raw, &in); err != nil {
			return nil, fmt.Errorf("regions: %v", err)
		}
		for _, r := range in {
			regs = append(regs, sketchRegion{ID: r.ID, Outer: r.Outer, Holes: r.Holes})
		}
		sp.regions = regs
	} else {
		for _, id := range sp.order {
			if !sp.ents[id].closed {
				return nil, fmt.Errorf("entity %q is open: list the closed loops in regions", id)
			}
			sp.regions = append(sp.regions, sketchRegion{ID: id, Outer: []string{id}})
		}
	}
	if len(sp.regions) == 0 {
		return nil, fmt.Errorf("sketch has no regions")
	}
	seen := map[string]bool{}
	for i, r := range sp.regions {
		if r.ID == "" {
			return nil, fmt.Errorf("regions[%d] needs an id", i)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("duplicate region id %q", r.ID)
		}
		seen[r.ID] = true
		if len(r.Outer) == 0 {
			return nil, fmt.Errorf("region %q has no outer loop", r.ID)
		}
	}
	return sp, nil
}

func segStart(s api.Seg2D) [2]float64 { return [2]float64{s.A[0], s.A[1]} }

func segEnd(s api.Seg2D) [2]float64 {
	switch s.Kind {
	case 1:
		return [2]float64{s.A[4], s.A[5]}
	case 3:
		return [2]float64{s.A[6], s.A[7]}
	}
	return [2]float64{s.A[2], s.A[3]}
}

func segFlip(s api.Seg2D) api.Seg2D {
	switch s.Kind {
	case 0:
		return api.Seg2D{Kind: 0, A: [8]float64{s.A[2], s.A[3], s.A[0], s.A[1]}}
	case 1:
		return api.Seg2D{Kind: 1, A: [8]float64{s.A[4], s.A[5], s.A[2], s.A[3], s.A[0], s.A[1]}}
	case 3:
		return api.Seg2D{Kind: 3, A: [8]float64{s.A[6], s.A[7], s.A[4], s.A[5], s.A[2], s.A[3], s.A[0], s.A[1]}}
	}
	return s
}

func near(a, b [2]float64) bool { return math.Hypot(a[0]-b[0], a[1]-b[1]) <= sketchTol }

// loop returns the ordered, end-to-start segments of a list of entities.
func (sp *sketchSpec) loop(ids []string) ([]api.Seg2D, error) {
	if len(ids) == 1 {
		e, ok := sp.ents[ids[0]]
		if !ok {
			return nil, fmt.Errorf("unknown entity %q", ids[0])
		}
		if e.closed {
			return e.segs, nil
		}
	}
	var pool []api.Seg2D
	for _, id := range ids {
		e, ok := sp.ents[id]
		if !ok {
			return nil, fmt.Errorf("unknown entity %q", id)
		}
		if e.closed {
			return nil, fmt.Errorf("entity %q is already a closed loop; it cannot be chained with others", id)
		}
		pool = append(pool, e.segs...)
	}
	chain := []api.Seg2D{pool[0]}
	pool = pool[1:]
	for len(pool) > 0 {
		end := segEnd(chain[len(chain)-1])
		found := -1
		flip := false
		for i, s := range pool {
			if near(segStart(s), end) {
				found = i
				break
			}
			if near(segEnd(s), end) {
				found, flip = i, true
				break
			}
		}
		if found < 0 {
			return nil, fmt.Errorf("loop is not connected near (%.4g, %.4g)", end[0], end[1])
		}
		s := pool[found]
		if flip {
			s = segFlip(s)
		}
		chain = append(chain, s)
		pool = append(pool[:found], pool[found+1:]...)
	}
	if !near(segEnd(chain[len(chain)-1]), segStart(chain[0])) {
		return nil, fmt.Errorf("loop is not closed")
	}
	return chain, nil
}

// regionFace builds one region (outer loop with holes) as a face in the sketch's local XY plane.
func (sp *sketchSpec) regionFace(k *api.Kernel, r sketchRegion) (*api.Shape, error) {
	var segs []api.Seg2D
	var sizes []int
	add := func(ids []string) error {
		l, err := sp.loop(ids)
		if err != nil {
			return err
		}
		segs = append(segs, l...)
		sizes = append(sizes, len(l))
		return nil
	}
	if err := add(r.Outer); err != nil {
		return nil, fmt.Errorf("region %q: %w", r.ID, err)
	}
	for _, h := range r.Holes {
		if err := add(h); err != nil {
			return nil, fmt.Errorf("region %q hole: %w", r.ID, err)
		}
	}
	f, err := k.MakeFace(segs, sizes)
	if err != nil {
		return nil, fmt.Errorf("region %q: %w", r.ID, err)
	}
	return f, nil
}

// sketchFrame is the placement frame of a sketch node: a world plane, or a planar face.
func sketchFrame(c *Ctx, n *cadir.Node, p Params) (Frame, error) {
	offset, err := p.NumOr("offset", 0)
	if err != nil {
		return Frame{}, err
	}
	if n.References["face"] != "" {
		d, b, err := faceOf(c, n, p)
		if err != nil {
			return Frame{}, err
		}
		fr, err := b.Shape.FaceFrame(d.Index)
		if err != nil {
			return Frame{}, fmt.Errorf("face %s: %w", d.Selector, err)
		}
		f := Frame{Origin: fr.Origin, Z: fr.Normal, X: fr.X, OnFace: true}
		for i := 0; i < 3; i++ {
			f.Origin[i] += fr.Normal[i] * offset
		}
		return f, nil
	}
	switch strings.ToUpper(p.Str("plane")) {
	case "", "XY":
		return Frame{Origin: [3]float64{0, 0, offset}, Z: [3]float64{0, 0, 1}, X: [3]float64{1, 0, 0}}, nil
	case "YZ": // local u→Y, v→Z, normal +X (same convention as extrude_profile)
		return Frame{Origin: [3]float64{offset, 0, 0}, Z: [3]float64{1, 0, 0}, X: [3]float64{0, 1, 0}}, nil
	case "XZ": // local u→Z, v→X, normal +Y
		return Frame{Origin: [3]float64{0, offset, 0}, Z: [3]float64{0, 1, 0}, X: [3]float64{0, 0, 1}}, nil
	}
	return Frame{}, fmt.Errorf("plane must be XY, XZ or YZ (got %q)", p.Str("plane"))
}

// Frame is the world placement of a sketch: origin, normal (local +Z) and local +X.
type Frame struct {
	Origin, Z, X [3]float64
	OnFace       bool
}

func buildSketch(c *Ctx) (*api.Shape, error) {
	sp, err := parseSketch(c.P)
	if err != nil {
		return nil, err
	}
	fr, err := sketchFrame(c, c.Node, c.P)
	if err != nil {
		return nil, err
	}
	a := &arena{}
	var cur *api.Shape
	for _, r := range sp.regions {
		f, err := a.add(sp.regionFace(c.K, r))
		if err != nil {
			a.releaseExcept(nil)
			return nil, err
		}
		if cur == nil {
			cur = f
			continue
		}
		cur, err = a.add(c.K.Union(cur, f))
		if err != nil {
			a.releaseExcept(nil)
			return nil, err
		}
	}
	placed, err := cur.Place(fr.Origin, fr.Z, fr.X)
	a.releaseExcept(nil)
	return placed, err
}

// sketchPrism extrudes a sketch node's regions (one region, or all of them fused) along the
// sketch normal over local w ∈ [w0, w0+depth], placed in the world.
func sketchPrism(c *Ctx, sketchID, region string, w0, depth float64, reverse bool) (*api.Shape, error) {
	sk := c.NodeOf(sketchID)
	if sk == nil || sk.Type != "sketch" {
		return nil, fmt.Errorf("node %q is not a sketch", sketchID)
	}
	if b := c.Bodies[sketchID]; b == nil || !b.Sketch {
		return nil, fmt.Errorf("sketch %q has no geometry", sketchID)
	}
	sp0, ok := c.ParamsOf(sketchID)
	if !ok {
		return nil, fmt.Errorf("sketch %q has no resolved parameters", sketchID)
	}
	sp, err := parseSketch(sp0)
	if err != nil {
		return nil, fmt.Errorf("sketch %q: %w", sketchID, err)
	}
	fr, err := sketchFrame(c, sk, sp0)
	if err != nil {
		return nil, fmt.Errorf("sketch %q: %w", sketchID, err)
	}
	var regs []sketchRegion
	for _, r := range sp.regions {
		if region == "" || r.ID == region {
			regs = append(regs, r)
		}
	}
	if len(regs) == 0 {
		return nil, fmt.Errorf("sketch %q has no region %q", sketchID, region)
	}
	if reverse {
		w0 = -w0 - depth
	}
	a := &arena{}
	var cur *api.Shape
	for _, r := range regs {
		f, err := a.add(sp.regionFace(c.K, r))
		if err != nil {
			a.releaseExcept(nil)
			return nil, err
		}
		solid, err := a.add(f.Prism(0, 0, depth))
		if err != nil {
			a.releaseExcept(nil)
			return nil, err
		}
		if solid, err = a.add(solid.Translate(0, 0, w0)); err != nil {
			a.releaseExcept(nil)
			return nil, err
		}
		if cur == nil {
			cur = solid
			continue
		}
		if cur, err = a.add(c.K.Union(cur, solid)); err != nil {
			a.releaseExcept(nil)
			return nil, err
		}
	}
	placed, err := cur.Place(fr.Origin, fr.Z, fr.X)
	a.releaseExcept(nil)
	return placed, err
}

// SketchRegionIDs lists a sketch's region ids (tools use it to validate extrude requests).
func SketchRegionIDs(p Params) ([]string, error) {
	sp, err := parseSketch(p)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(sp.regions))
	for i, r := range sp.regions {
		ids[i] = r.ID
	}
	return ids, nil
}

// ValidateSketch checks a sketch's parameters without touching the kernel.
func ValidateSketch(p Params) error {
	sp, err := parseSketch(p)
	if err != nil {
		return err
	}
	for _, r := range sp.regions {
		if _, err := sp.loop(r.Outer); err != nil {
			return fmt.Errorf("region %q: %w", r.ID, err)
		}
		for _, h := range r.Holes {
			if _, err := sp.loop(h); err != nil {
				return fmt.Errorf("region %q hole: %w", r.ID, err)
			}
		}
	}
	return nil
}
