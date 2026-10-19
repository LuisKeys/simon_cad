package vectorize

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
)

// dxfFile builds an ASCII DXF: units is $INSUNITS, entities is the ENTITIES body, extra goes
// before it (a BLOCKS or TABLES section).
func dxfFile(units int, extra, entities string) []byte {
	var b strings.Builder
	b.WriteString("0\nSECTION\n2\nHEADER\n9\n$INSUNITS\n70\n" + fmt.Sprint(units) + "\n0\nENDSEC\n")
	b.WriteString(extra)
	b.WriteString("0\nSECTION\n2\nENTITIES\n" + entities + "0\nENDSEC\n0\nEOF\n")
	return []byte(b.String())
}

func dxfLine(x0, y0, x1, y1 float64) string {
	return fmt.Sprintf("0\nLINE\n8\n0\n10\n%g\n20\n%g\n30\n0\n11\n%g\n21\n%g\n31\n0\n", x0, y0, x1, y1)
}

func dxfCircle(x, y, r float64) string {
	return fmt.Sprintf("0\nCIRCLE\n8\n0\n10\n%g\n20\n%g\n40\n%g\n", x, y, r)
}

func runDXF(t *testing.T, data []byte, opt Options) *Result {
	t.Helper()
	s, err := ParseDXF(data)
	if err != nil {
		t.Fatal(err)
	}
	if s.NativePxPerMM > 0 {
		opt.PxPerMM = s.NativePxPerMM
	} else if opt.PxPerMM == 0 {
		opt.PxPerMM = 1
	}
	r, err := RunSVG(context.Background(), s, opt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDXFSniffing(t *testing.T) {
	if !IsDXF(dxfFile(4, "", "")) || !IsDXF([]byte("999\ncomment\r\n  0\r\nSECTION\r\n  2\r\nHEADER\r\n")) {
		t.Fatal("a DXF was not recognised")
	}
	if IsDXF([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)) || IsDXF([]byte("\x89PNG\r\n")) {
		t.Fatal("something else was taken for a DXF")
	}
	if _, err := ParseDXF([]byte("AutoCAD Binary DXF\r\n\x1a\x00")); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary DXF: %v", err)
	}
}

func TestDXFLooseLinesJoinIntoAFilledRectangle(t *testing.T) {
	// 40 × 20 mm drawn as four lines, out of order and one reversed, with a 6 mm hole
	ents := dxfLine(40, 0, 40, 20) + dxfLine(0, 0, 40, 0) + dxfLine(0, 20, 40, 20) + dxfLine(0, 0, 0, 20) + dxfCircle(20, 10, 3)
	r := runDXF(t, dxfFile(4, "", ents), Options{Mode: FilledRegions})
	if len(r.Regions) != 1 || len(r.Regions[0].Holes) != 1 {
		t.Fatalf("regions %d holes %d, warnings %v", len(r.Regions), len(r.Regions[0].Holes), r.Warnings)
	}
	within(t, "area (mm²)", r.Regions[0].Area, 40*20-math.Pi*9, 0.01)
	if len(r.Warnings) != 0 {
		t.Fatalf("warnings %v", r.Warnings)
	}
}

func TestDXFUnitsAreConvertedAndY_IsUp(t *testing.T) {
	// 2 × 1 inch rectangle (INSUNITS 1) with its lower-left at (10, 10)
	ents := "0\nLWPOLYLINE\n8\n0\n90\n4\n70\n1\n10\n10\n20\n10\n10\n12\n20\n10\n10\n12\n20\n11\n10\n10\n20\n11\n"
	r := runDXF(t, dxfFile(1, "", ents), Options{Mode: FilledRegions})
	within(t, "area (mm²)", r.Regions[0].Area, 50.8*25.4, 1e-6)
	minX, minY, maxY := math.Inf(1), math.Inf(1), math.Inf(-1)
	for _, g := range r.Regions[0].Outer {
		minX, minY, maxY = math.Min(minX, g.P0[0]), math.Min(minY, g.P0[1]), math.Max(maxY, g.P0[1])
	}
	within(t, "min x", minX, 0, 1e-6)
	within(t, "min y", minY, 0, 1e-6)
	within(t, "height", maxY, 25.4, 1e-6)
}

func TestDXFBulgesGiveArcs(t *testing.T) {
	// a stadium: two semicircles (bulge 1) joined by straight sides; width 10, length 30
	ents := "0\nLWPOLYLINE\n90\n4\n70\n1\n10\n5\n20\n0\n10\n25\n20\n0\n42\n1\n10\n25\n20\n10\n10\n5\n20\n10\n42\n1\n"
	r := runDXF(t, dxfFile(4, "", ents), Options{Mode: FilledRegions})
	within(t, "area", r.Regions[0].Area, 20*10+math.Pi*25, 5e-3)
}

func TestDXFArcsAndNegativeBulgeAndMirroredExtrusion(t *testing.T) {
	// a half disc from an ARC (0..180°) closed by a LINE
	arc := "0\nARC\n10\n0\n20\n0\n40\n10\n50\n0\n51\n180\n"
	r := runDXF(t, dxfFile(4, "", arc+dxfLine(-10, 0, 10, 0)), Options{Mode: FilledRegions})
	within(t, "half disc", r.Regions[0].Area, math.Pi*50, 5e-3)
	// an arc with extrusion -Z is mirrored in X: 0..90° becomes the quadrant 90..180°
	mir := "0\nARC\n10\n0\n20\n0\n40\n10\n50\n0\n51\n90\n210\n0\n220\n0\n230\n-1\n"
	r = runDXF(t, dxfFile(4, "", mir+dxfLine(0, 10, 0, 0)+dxfLine(0, 0, -10, 0)), Options{Mode: FilledRegions})
	if len(r.Regions) != 1 {
		t.Fatalf("mirrored quadrant did not close: %v", r.Warnings)
	}
	within(t, "quadrant", r.Regions[0].Area, math.Pi*25, 5e-3)
	// negative bulge turns clockwise: the same stadium with the bulges reversed bulges inwards
	ents := "0\nLWPOLYLINE\n90\n4\n70\n1\n10\n5\n20\n0\n10\n25\n20\n0\n42\n-1\n10\n25\n20\n10\n10\n5\n20\n10\n42\n-1\n"
	r = runDXF(t, dxfFile(4, "", ents), Options{Mode: FilledRegions})
	within(t, "dented", r.Regions[0].Area, 20*10-math.Pi*25, 5e-3)
}

func TestDXFBlocksInsertWithRotationScaleAndArray(t *testing.T) {
	blocks := "0\nSECTION\n2\nBLOCKS\n0\nBLOCK\n2\nSQ\n10\n0\n20\n0\n" +
		dxfLine(0, 0, 1, 0) + dxfLine(1, 0, 1, 1) + dxfLine(1, 1, 0, 1) + dxfLine(0, 1, 0, 0) + "0\nENDBLK\n0\nENDSEC\n"
	// 2 columns × 1 row of 4×4 squares rotated by 90° about their insertion point (10, 0)
	ins := "0\nINSERT\n2\nSQ\n10\n10\n20\n0\n41\n4\n42\n4\n50\n90\n70\n2\n71\n1\n44\n20\n"
	r := runDXF(t, dxfFile(4, blocks, ins), Options{Mode: FilledRegions})
	if len(r.Regions) != 2 {
		t.Fatalf("regions %d", len(r.Regions))
	}
	for _, rg := range r.Regions {
		within(t, "square", rg.Area, 16, 1e-6)
	}
}

func TestDXFSplineThroughControlPoints(t *testing.T) {
	// a closed-profile: a quadratic spline (a parabola arc, 0..10) closed by a line
	sp := "0\nSPLINE\n70\n8\n71\n2\n72\n6\n73\n3\n40\n0\n40\n0\n40\n0\n40\n1\n40\n1\n40\n1\n10\n0\n20\n0\n10\n5\n20\n20\n10\n10\n20\n0\n"
	r := runDXF(t, dxfFile(4, "", sp+dxfLine(10, 0, 0, 0)), Options{Mode: FilledRegions})
	// area under the parabola y = 20·(x/5)(1-x/10)·... = 2/3 · base · height = 2/3 · 10 · 10
	within(t, "spline area", r.Regions[0].Area, 2.0/3*10*10, 1e-3)
}

func TestDXFWarnsAboutOpenChainsAndIgnoredEntities(t *testing.T) {
	ents := dxfCircle(0, 0, 5) + dxfLine(20, 0, 30, 0) + "0\nTEXT\n8\n0\n10\n0\n20\n0\n1\nhello\n0\nHATCH\n8\n0\n"
	s, err := ParseDXF(dxfFile(4, "", ents))
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(s.Warnings, "\n")
	for _, want := range []string{"1 open chain", "1 TEXT not imported", "1 HATCH not imported"} {
		if !strings.Contains(w, want) {
			t.Fatalf("missing %q in\n%s", want, w)
		}
	}
	r, err := RunSVG(context.Background(), s, Options{Mode: Centerline, PxPerMM: s.NativePxPerMM})
	if err != nil || len(r.Strokes) != 2 {
		t.Fatalf("centerline %v strokes %d", err, len(r.Strokes))
	}
}

func TestDXFHiddenLayersAndPaperSpaceAreSkipped(t *testing.T) {
	tables := "0\nSECTION\n2\nTABLES\n0\nTABLE\n2\nLAYER\n0\nLAYER\n2\nHIDE\n70\n1\n62\n7\n0\nENDTAB\n0\nENDSEC\n"
	ents := dxfCircle(0, 0, 5) + "0\nCIRCLE\n8\nHIDE\n10\n50\n20\n0\n40\n5\n" + "0\nCIRCLE\n67\n1\n10\n80\n20\n0\n40\n5\n"
	r := runDXF(t, dxfFile(4, tables, ents), Options{Mode: FilledRegions})
	if len(r.Regions) != 1 {
		t.Fatalf("regions %d", len(r.Regions))
	}
}

func TestDXFUnknownUnitsKeepDrawingUnits(t *testing.T) {
	s, err := ParseDXF(dxfFile(0, "", dxfCircle(0, 0, 5)))
	if err != nil {
		t.Fatal(err)
	}
	if s.NativePxPerMM != 0 || math.Abs(s.Width-10) > 1e-9 {
		t.Fatalf("native %v width %v", s.NativePxPerMM, s.Width)
	}
}

func TestDXFRejectsGarbage(t *testing.T) {
	for name, in := range map[string]string{
		"not pairs":  "0\nSECTION\nhello\nworld\n",
		"empty":      string(dxfFile(4, "", "")),
		"no section": "hello",
	} {
		if _, err := ParseDXF([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
