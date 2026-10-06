package vision

import (
	"fmt"
	"math"
	"sort"
)

// Scale is the millimetre size of one image pixel. It is only ever derived from a
// dimension a human wrote or confirmed, or set explicitly — never from the image's pixel size.
type Scale struct {
	MMPerPx float64 `json:"mm_per_px"`
	Source  string  `json:"source"` // explicit | dimension
}

// measurePx measures a bindable entity field in image pixels.
func measurePx(e Entity, field string) (float64, bool) {
	switch e.Kind() {
	case "rectangle":
		return e.num(field)
	case "circle":
		r, ok := e.num("r")
		switch field {
		case "radius":
			return r, ok
		case "diameter":
			return 2 * r, ok
		}
	case "slot":
		switch field {
		case "width":
			return e.num("width")
		case "length":
			x1, ok1 := e.num("x1")
			y1, ok2 := e.num("y1")
			x2, ok3 := e.num("x2")
			y2, ok4 := e.num("y2")
			return math.Hypot(x2-x1, y2-y1), ok1 && ok2 && ok3 && ok4
		}
	case "line":
		if field == "length" {
			a, ok1 := e.pt("p1")
			b, ok2 := e.pt("p2")
			return dist(a, b), ok1 && ok2
		}
	}
	return 0, false
}

// trusted reports whether a dimension's value may be used without asking: it has a value
// and was confirmed, supplied by the user, or read with enough confidence.
func (d *Dimension) trusted() bool {
	if d.Value == nil || *d.Value <= 0 {
		return false
	}
	switch d.Source {
	case "confirmed", "user":
		return true
	case "missing":
		return false
	}
	return d.Confidence >= ConfidenceThreshold
}

// bindParts splits "e1.w" into entity id and field.
func (d *Dimension) bindParts() (id, field string, ok bool) {
	m := bindPattern.FindStringSubmatch(d.Binds)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ResolveScale picks the mm-per-pixel scale: an explicit scale wins; otherwise the mean of
// value/measured pixels over every trusted, measurable dimension. Disagreement above 5%
// between sources is reported as a warning rather than silently averaged away.
func ResolveScale(p *Proposal, explicit *Scale) (*Scale, []string) {
	var warns []string
	var ratios []float64
	var ids []string
	for i := range p.Dimensions {
		d := &p.Dimensions[i]
		if !d.trusted() || d.Kind == "depth" {
			continue
		}
		var px float64
		if id, field, ok := d.bindParts(); ok {
			e := p.entity(id)
			if e == nil {
				continue
			}
			m, ok := measurePx(e, field)
			if !ok || m <= 0 {
				continue
			}
			px = m
		} else if d.MeasuredPx != nil && *d.MeasuredPx > 0 {
			px = *d.MeasuredPx
		} else {
			continue
		}
		ratios = append(ratios, *d.Value/px)
		ids = append(ids, d.ID)
	}
	var fromDims float64
	if len(ratios) > 0 {
		var sum float64
		for _, r := range ratios {
			sum += r
		}
		fromDims = sum / float64(len(ratios))
		sorted := append([]float64(nil), ratios...)
		sort.Float64s(sorted)
		if sorted[len(sorted)-1]/sorted[0] > 1.05 {
			warns = append(warns, fmt.Sprintf("dimensions %v imply scales that differ by more than 5%% (%.4g..%.4g mm/px); check them",
				ids, sorted[0], sorted[len(sorted)-1]))
		}
	}
	if explicit != nil && explicit.MMPerPx > 0 {
		if fromDims > 0 && math.Abs(fromDims-explicit.MMPerPx)/explicit.MMPerPx > 0.05 {
			warns = append(warns, fmt.Sprintf("the explicit scale (%.4g mm/px) disagrees with the dimensions (%.4g mm/px)", explicit.MMPerPx, fromDims))
		}
		return &Scale{MMPerPx: explicit.MMPerPx, Source: "explicit"}, warns
	}
	if fromDims > 0 {
		return &Scale{MMPerPx: fromDims, Source: "dimension"}, warns
	}
	return nil, warns
}
