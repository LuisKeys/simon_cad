package vectorize

import (
	"context"
	"math"
	"strings"
	"testing"
)

const logoSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100" viewBox="0 0 200 100">
 <rect x="10" y="10" width="80" height="80" fill="#000"/>
 <circle cx="50" cy="50" r="20" fill="#000" transform="translate(0 0)"/>
 <g fill="none" stroke="#123" stroke-width="3">
   <path d="M120 90 C 120 40, 180 40, 180 90"/>
 </g>
</svg>`

func TestParseSVGShapes(t *testing.T) {
	s, err := ParseSVG([]byte(logoSVG))
	if err != nil {
		t.Fatal(err)
	}
	if s.Width != 200 || s.Height != 100 {
		t.Fatalf("size %v x %v", s.Width, s.Height)
	}
	if len(s.Paths) != 3 {
		t.Fatalf("paths %d", len(s.Paths))
	}
	if !s.Paths[0].Fill || !s.Paths[0].Closed || len(s.Paths[0].Segs) != 4 {
		t.Fatalf("rect %+v", s.Paths[0])
	}
	if s.Paths[2].Fill || !s.Paths[2].Stroke || math.Abs(s.Paths[2].StrokeWidth-3) > 1e-9 {
		t.Fatalf("stroked path %+v", s.Paths[2])
	}
}

func TestSVGFilledRegionsKeepHolesAndScale(t *testing.T) {
	// a ring made by nesting (outer circle + inner circle drawn as separate fills)
	src := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
	  <path fill-rule="evenodd" d="M50 10 A40 40 0 1 1 50 90 A40 40 0 1 1 50 10 Z M50 30 A20 20 0 1 0 50 70 A20 20 0 1 0 50 30 Z"/>
	</svg>`
	s, err := ParseSVG([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	r, err := RunSVG(context.Background(), s, Options{Mode: FilledRegions, PxPerMM: 2}) // 100 units = 50 mm
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Regions) != 1 || len(r.Regions[0].Holes) != 1 {
		t.Fatalf("regions %d", len(r.Regions))
	}
	within(t, "ring area (mm²)", r.Regions[0].Area, math.Pi*(20*20-10*10), 0.01)
	// Y is flipped to point up: the top of the circle (SVG y=10) is at y = (100-10)/2 = 45 mm
	maxY := math.Inf(-1)
	for _, g := range r.Regions[0].Outer {
		for u := 0.0; u <= 1; u += 0.05 {
			maxY = math.Max(maxY, g.At(u)[1])
		}
	}
	if math.Abs(maxY-45) > 0.1 {
		t.Fatalf("top at %v want 45", maxY)
	}
}

func TestSVGCenterlineUsesStrokeWidth(t *testing.T) {
	s, _ := ParseSVG([]byte(logoSVG))
	r, err := RunSVG(context.Background(), s, Options{Mode: Centerline, PxPerMM: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Strokes) != 3 {
		t.Fatalf("strokes %d", len(r.Strokes))
	}
	last := r.Strokes[2]
	if last.Closed || len(last.Widths) != len(last.Segs)+1 || math.Abs(last.Widths[0]-3) > 1e-9 {
		t.Fatalf("%+v", last)
	}
	// a cubic from (120,90) to (180,90) bulging up to y=52.5: its length is between the chord and the control polygon
	if last.Length < 60 || last.Length > 135 {
		t.Fatalf("length %v", last.Length)
	}
}

func TestSVGTransformsAndArcs(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100">
	  <g transform="translate(10 20) scale(2)"><rect x="0" y="0" width="10" height="5"/></g>
	  <path d="M0 0 a10 10 0 0 1 20 0" transform="rotate(90)" stroke="#000" fill="none"/>
	</svg>`
	s, err := ParseSVG([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := s.Paths[0].Segs[0].P0, s.Paths[0].Segs[2].P0
	if lo[0] != 10 || lo[1] != 20 || hi[0] != 30 || hi[1] != 30 {
		t.Fatalf("rect corners %v %v", lo, hi)
	}
	// the half-circle arc of radius 10 rotated by 90°: it ends at (0, 20)
	arc := s.Paths[1].Segs
	end := arc[len(arc)-1].P1
	if math.Abs(end[0]) > 1e-6 || math.Abs(end[1]-20) > 1e-6 {
		t.Fatalf("arc end %v", end)
	}
	mid := arc[0].P1
	if math.Abs(math.Hypot(mid[0], mid[1]-10)-10) > 1e-6 {
		t.Fatalf("arc point not on the circle: %v", mid)
	}
}

func TestSVGErrorsAreReadable(t *testing.T) {
	for _, bad := range []string{"", "<html></html>", `<svg><path d="M 0 0 L"/></svg>`, `<svg><path d="X 1 2"/></svg>`, `<svg><g transform="skew(3)"><rect width="1" height="1"/></g></svg>`} {
		if _, err := ParseSVG([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	empty, _ := ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	if _, err := RunSVG(context.Background(), empty, Options{}); err == nil || !strings.Contains(err.Error(), "VECTORIZE_EMPTY") {
		t.Fatalf("empty svg: %v", err)
	}
}

func TestSVGPathCommandsCompact(t *testing.T) {
	// compact number forms: "1.5.5", "1e1", flags without separators, implicit repeats
	subs, err := parsePathData("M0,0 l1.5.5 2 3 4 5 H10 v-5 s1 1 2 2 q1 1 2 0 t2 0 a1 1 0 01.5.5z")
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || !subs[0].Closed || len(subs[0].Segs) < 6 {
		t.Fatalf("%+v", subs)
	}
}
