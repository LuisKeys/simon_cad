package vision

import (
	"fmt"
	"math"
)

// closedPoly returns the outline of an entity that is a loop on its own.
func closedPoly(e Entity) ([]pt, bool) {
	switch e.Kind() {
	case "circle":
		cx, ok1 := e.num("cx")
		cy, ok2 := e.num("cy")
		r, ok3 := e.num("r")
		if !ok1 || !ok2 || !ok3 || r <= 0 {
			return nil, false
		}
		return circlePoly(pt{cx, cy}, r, 48), true
	case "rectangle":
		x, ok1 := e.num("x")
		y, ok2 := e.num("y")
		w, ok3 := e.num("w")
		h, ok4 := e.num("h")
		if !ok1 || !ok2 || !ok3 || !ok4 || w <= 0 || h <= 0 {
			return nil, false
		}
		return []pt{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}, true
	case "polygon":
		raw, ok := e["points"].([]any)
		if !ok || len(raw) < 3 {
			return nil, false
		}
		out := make([]pt, 0, len(raw))
		for _, it := range raw {
			a, ok := it.([]any)
			if !ok || len(a) != 2 {
				return nil, false
			}
			x, ok1 := a[0].(float64)
			y, ok2 := a[1].(float64)
			if !ok1 || !ok2 {
				return nil, false
			}
			out = append(out, pt{x, y})
		}
		return out, true
	case "slot":
		x1, ok1 := e.num("x1")
		y1, ok2 := e.num("y1")
		x2, ok3 := e.num("x2")
		y2, ok4 := e.num("y2")
		w, ok5 := e.num("width")
		l := math.Hypot(x2-x1, y2-y1)
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || w <= 0 || l < 1e-9 {
			return nil, false
		}
		r := w / 2
		ux, uy := (x2-x1)/l, (y2-y1)/l
		nx, ny := -uy, ux
		var out []pt
		// half circle around p2 (from the left side to the right side through the tip), then around p1
		for i := 0; i <= 12; i++ {
			t := math.Pi/2 - math.Pi*float64(i)/12 // +n ... +u ... -n
			out = append(out, pt{x2 + r*(nx*math.Sin(t)+ux*math.Cos(t)), y2 + r*(ny*math.Sin(t)+uy*math.Cos(t))})
		}
		for i := 0; i <= 12; i++ {
			t := -math.Pi/2 - math.Pi*float64(i)/12 // -n ... -u ... +n
			out = append(out, pt{x1 + r*(nx*math.Sin(t)+ux*math.Cos(t)), y1 + r*(ny*math.Sin(t)+uy*math.Cos(t))})
		}
		return out, true
	}
	return nil, false
}

// openPts is the polyline of a line or arc entity.
func openPts(e Entity) ([]pt, bool) {
	switch e.Kind() {
	case "line":
		a, ok1 := e.pt("p1")
		b, ok2 := e.pt("p2")
		return []pt{a, b}, ok1 && ok2
	case "arc":
		a, ok1 := e.pt("p1")
		m, ok2 := e.pt("mid")
		b, ok3 := e.pt("p2")
		if !ok1 || !ok2 || !ok3 {
			return nil, false
		}
		if _, _, ok := arcCircle(a, m, b); !ok {
			return nil, false
		}
		return arcPoints(a, m, b, 16), true
	}
	return nil, false
}

func dist(a, b pt) float64 { return math.Hypot(a[0]-b[0], a[1]-b[1]) }

// chainOrder orders open entities into a loop by nearest endpoints. It returns for each
// position the entity id and whether it is traversed backwards, the polygon, and the
// largest gap between consecutive endpoints (including the closing gap).
type chainStep struct {
	ID      string
	Reverse bool
}

func chainLoop(p *Proposal, ids []string) (steps []chainStep, poly []pt, maxGap float64, err error) {
	type seg struct {
		id  string
		pts []pt
	}
	var pool []seg
	for _, id := range ids {
		e := p.entity(id)
		if e == nil {
			return nil, nil, 0, fmt.Errorf("unknown entity %q", id)
		}
		pts, ok := openPts(e)
		if !ok {
			return nil, nil, 0, fmt.Errorf("entity %q (%s) cannot be part of a chained loop", id, e.Kind())
		}
		pool = append(pool, seg{id, pts})
	}
	if len(pool) < 2 {
		return nil, nil, 0, fmt.Errorf("a loop of open entities needs at least 2 entities")
	}
	cur := pool[0]
	pool = pool[1:]
	steps = []chainStep{{cur.id, false}}
	poly = append(poly, cur.pts...)
	for len(pool) > 0 {
		end := poly[len(poly)-1]
		best, bestD, rev := -1, math.Inf(1), false
		for i, s := range pool {
			if d := dist(end, s.pts[0]); d < bestD {
				best, bestD, rev = i, d, false
			}
			if d := dist(end, s.pts[len(s.pts)-1]); d < bestD {
				best, bestD, rev = i, d, true
			}
		}
		s := pool[best]
		pool = append(pool[:best], pool[best+1:]...)
		pts := s.pts
		if rev {
			pts = make([]pt, len(s.pts))
			for i, q := range s.pts {
				pts[len(pts)-1-i] = q
			}
		}
		maxGap = math.Max(maxGap, bestD)
		steps = append(steps, chainStep{s.id, rev})
		poly = append(poly, pts...)
	}
	maxGap = math.Max(maxGap, dist(poly[len(poly)-1], poly[0]))
	return steps, poly, maxGap, nil
}

// loopOutline returns the polygon of a loop (one closed entity, or a chain) and its largest gap.
func loopOutline(p *Proposal, ids []string) ([]pt, float64, error) {
	if len(ids) == 0 {
		return nil, 0, fmt.Errorf("empty loop")
	}
	if len(ids) == 1 {
		e := p.entity(ids[0])
		if e == nil {
			return nil, 0, fmt.Errorf("unknown entity %q", ids[0])
		}
		if poly, ok := closedPoly(e); ok {
			return poly, 0, nil
		}
		return nil, 0, fmt.Errorf("entity %q (%s) is open and cannot form a loop alone", ids[0], e.Kind())
	}
	for _, id := range ids {
		if e := p.entity(id); e != nil {
			if _, closed := closedPoly(e); closed {
				return nil, 0, fmt.Errorf("closed entity %q cannot be chained with other entities", id)
			}
		}
	}
	_, poly, gap, err := chainLoop(p, ids)
	return poly, gap, err
}
