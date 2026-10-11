package vectorize

import (
	"fmt"
	"math"
	"strings"
)

func f3(v float64) string { return fmt.Sprintf("%.3f", v) }

func segPath(segs []Seg, closed bool) string {
	if len(segs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "M%s %s", f3(segs[0].P0[0]), f3(segs[0].P0[1]))
	for _, s := range segs {
		fmt.Fprintf(&b, "C%s %s %s %s %s %s", f3(s.C1[0]), f3(s.C1[1]), f3(s.C2[0]), f3(s.C2[1]), f3(s.P1[0]), f3(s.P1[1]))
	}
	if closed {
		b.WriteString("Z")
	}
	return b.String()
}

// SVG draws the result as an SVG document in the output units (Y up is flipped back so it looks
// like the source): regions filled with their holes, outlines stroked, strokes at their width.
// It is a preview for the UI and for checking the trace; the geometry is Result itself.
func (r *Result) SVG() string {
	w, h := float64(r.WidthPx)*r.Scale, float64(r.HeightPx)*r.Scale
	var b strings.Builder
	// flip Y so the picture is upright: SVG y = h - y
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %s %s" width="%s" height="%s"><g transform="translate(0 %s) scale(1 -1)">`,
		f3(w), f3(h), f3(w), f3(h), f3(h))
	for _, rg := range r.Regions {
		var d strings.Builder
		d.WriteString(segPath(rg.Outer, true))
		for _, hole := range rg.Holes {
			d.WriteString(segPath(hole, true))
		}
		fmt.Fprintf(&b, `<path fill="#4c6fff" fill-opacity="0.55" fill-rule="evenodd" stroke="#1d3bd1" stroke-width="%s" d="%s"/>`, f3(math.Max(w, h)/600), d.String())
	}
	for _, l := range r.Outlines {
		color := "#1d3bd1"
		if l.Hole {
			color = "#d13b1d"
		}
		fmt.Fprintf(&b, `<path fill="none" stroke="%s" stroke-width="%s" d="%s"/>`, color, f3(math.Max(w, h)/400), segPath(l.Segs, true))
	}
	for _, s := range r.Strokes {
		avg := 0.0
		for _, v := range s.Widths {
			avg += v
		}
		if len(s.Widths) > 0 {
			avg /= float64(len(s.Widths))
		}
		fmt.Fprintf(&b, `<path fill="none" stroke="#1d3bd1" stroke-opacity="0.6" stroke-linecap="round" stroke-width="%s" d="%s"/>`, f3(math.Max(avg, math.Max(w, h)/600)), segPath(s.Segs, s.Closed))
	}
	b.WriteString("</g></svg>")
	return b.String()
}
