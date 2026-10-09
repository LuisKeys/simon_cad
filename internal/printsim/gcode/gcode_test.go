package gcode

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

func parse(t *testing.T, src string) *toolpath.SliceResult {
	t.Helper()
	r, err := Parse(context.Background(), strings.NewReader(src), Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Finalize(toolpath.FinalizeParams{FilamentDiameter: 1.75, FilamentDensity: 1.24, LayerHeight: 0.2, Nozzle: 0.4})
	return r
}

func parseErr(t *testing.T, src string) error {
	t.Helper()
	_, err := Parse(context.Background(), strings.NewReader(src), Options{})
	if err == nil {
		t.Fatalf("expected an error for %q", src)
	}
	return err
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-4 }

func TestAbsoluteMovesAndFeed(t *testing.T) {
	r := parse(t, "G90\nM82\nG92 E0\nG1 Z0.2 F600\nG1 X10 Y0 E1 F1200\nG1 X10 Y10 E2\n")
	if len(r.Segments) != 3 {
		t.Fatalf("segments: %d", len(r.Segments))
	}
	s := r.Segments[1]
	if s.X1 != 10 || s.E != 1 || !near(float64(s.F), 20) {
		t.Fatalf("segment 1: %+v", s)
	}
	if r.Segments[2].F != 20 { // feed is modal
		t.Fatalf("modal feed lost: %v", r.Segments[2].F)
	}
	if r.Segments[0].Kind != toolpath.Travel || r.Segments[1].Kind != toolpath.Other {
		t.Fatalf("kinds: %v %v", r.Segments[0].Kind, r.Segments[1].Kind)
	}
	if !near(r.FilamentMM, 2) {
		t.Fatalf("filament %v", r.FilamentMM)
	}
}

func TestRelativeXYZAndExtrusion(t *testing.T) {
	r := parse(t, "G90\nG1 X5 Y5 Z0.2\nG91\nM83\nG1 X10 E0.5\nG1 Y10 E0.5\nG90\nG1 X0 Y0\n")
	if r.Segments[1].X1 != 15 || r.Segments[1].Y1 != 5 {
		t.Fatalf("relative move: %+v", r.Segments[1])
	}
	if r.Segments[2].Y1 != 15 {
		t.Fatalf("second relative move: %+v", r.Segments[2])
	}
	if r.Segments[3].X1 != 0 || r.Segments[3].Y1 != 0 || r.Segments[3].Kind != toolpath.Travel {
		t.Fatalf("back to absolute: %+v", r.Segments[3])
	}
	if !near(r.FilamentMM, 1) {
		t.Fatalf("relative E total %v", r.FilamentMM)
	}
}

func TestAbsoluteExtrusionResetByG92(t *testing.T) {
	r := parse(t, "M82\nG92 E0\nG1 X10 E1\nG92 E0\nG1 X20 E1\nG1 X30 E2\n")
	// without the reset the second move would be a retraction of 0 or a 0-delta
	for i, want := range []float32{1, 1, 1} {
		if r.Segments[i].E != want {
			t.Fatalf("segment %d E = %v, want %v", i, r.Segments[i].E, want)
		}
	}
}

func TestRetractionAndUnretract(t *testing.T) {
	src := "M83\nG1 X0 Y0 Z0.2\nG1 X10 E1\n" +
		"G1 E-0.8 F2100\n" + // retract in place
		"G1 X50 Y50\n" + // travel
		"G1 E0.8\n" + // unretract in place
		"G1 X60 E1\n"
	r := parse(t, src)
	var dep, retr float64
	for _, s := range r.Segments {
		if s.Kind.Extrudes() {
			dep += float64(s.E)
		}
		if s.E < 0 {
			retr += float64(s.E)
			if s.Kind != toolpath.Travel {
				t.Fatalf("a retraction must be a travel segment: %+v", s)
			}
		}
	}
	if !near(dep, 2) || !near(retr, -0.8) {
		t.Fatalf("deposited %v retracted %v", dep, retr)
	}
	if !near(r.FilamentMM, 2) { // retract and unretract cancel
		t.Fatalf("net filament %v", r.FilamentMM)
	}
}

func TestUnretractRidingOnADepositionMove(t *testing.T) {
	// the combined move deposits 1.0 and refills 0.8: the bead must be sized by 1.0
	r := parse(t, "M83\nG1 X0 Y0 Z0.2\nG1 E-0.8\nG1 X10 E1.8\n")
	var dep *toolpath.Segment
	for i := range r.Segments {
		if r.Segments[i].Kind.Extrudes() {
			dep = &r.Segments[i]
		}
	}
	if dep == nil || !near(float64(dep.E), 1) {
		t.Fatalf("deposition segment %+v", dep)
	}
	if !near(r.FilamentMM, 1) {
		t.Fatalf("net filament %v", r.FilamentMM)
	}
}

func TestG92PositionOffset(t *testing.T) {
	r := parse(t, "G90\nG1 X10 Y10 Z1\nG92 X0 Y0\nG1 X5\nG91\nG1 X1\n")
	if r.Segments[1].X1 != 15 || r.Segments[1].Y1 != 10 { // X5 in the shifted frame is physical 15
		t.Fatalf("offset move: %+v", r.Segments[1])
	}
	if r.Segments[2].X1 != 16 {
		t.Fatalf("relative after offset: %+v", r.Segments[2])
	}
}

func TestArcsAreDiscretized(t *testing.T) {
	// half circle of radius 10 about (10,0), counter-clockwise from (0,0) to (20,0)... G3 from (0,0) to (20,0), I10
	r := parse(t, "M83\nG90\nG1 X0 Y0 Z0.2\nG3 X20 Y0 I10 J0 E2\n")
	n := len(r.Segments) - 1
	if n < 8 {
		t.Fatalf("an arc of r=10 must be split into several chords, got %d", n)
	}
	var e float64
	for _, s := range r.Segments[1:] {
		e += float64(s.E)
		// every chord end lies on the circle
		if d := math.Hypot(float64(s.X1)-10, float64(s.Y1)); math.Abs(d-10) > 1e-3 {
			t.Fatalf("point off the circle: %+v (r=%v)", s, d)
		}
	}
	if !near(e, 2) {
		t.Fatalf("arc extrusion %v", e)
	}
	last := r.Segments[len(r.Segments)-1]
	if last.X1 != 20 || last.Y1 != 0 {
		t.Fatalf("arc must end exactly at the target: %+v", last)
	}
	if r.Segments[1].Y1 >= 0 { // G3 is counter-clockwise: from the left of the centre it heads toward -Y
		t.Fatalf("G3 from the left of the centre must start toward -Y: %+v", r.Segments[1])
	}
}

func TestFullCircle(t *testing.T) {
	r := parse(t, "M83\nG90\nG1 X10 Y0 Z0.2\nG2 X10 Y0 I-10 J0 E3\n")
	if len(r.Segments) < 20 {
		t.Fatalf("a full circle must not collapse to a point: %d segments", len(r.Segments))
	}
}

func TestFeatureTypesAndLayers(t *testing.T) {
	src := `; generated by PrusaSlicer 2.7.0 on 2026-01-01
; estimated printing time (normal mode) = 1h 2m 3s
M83
G1 X0 Y0 Z0.3
G1 X20 E1
;LAYER_CHANGE
;Z:0.2
G1 Z0.2
;TYPE:External perimeter
G1 X10 Y0 E0.5
G1 X10 Y10 E0.5
;TYPE:Internal infill
G1 X0 Y10 E0.5
;LAYER_CHANGE
;Z:0.4
G1 Z0.4
;TYPE:Support material
G1 X0 Y0 E0.5
;TYPE:Something new
G1 X5 Y0 E0.5
`
	r := parse(t, src)
	if r.SlicerEstimate != 3723 {
		t.Fatalf("slicer estimate %v", r.SlicerEstimate)
	}
	if !strings.Contains(r.Version, "PrusaSlicer 2.7.0") {
		t.Fatalf("version %q", r.Version)
	}
	// prep (purge line) + 2 real layers
	if len(r.Layers) != 3 {
		t.Fatalf("layers: %+v", r.Layers)
	}
	if !near(float64(r.Layers[1].Z), 0.2) || !near(float64(r.Layers[2].Z), 0.4) {
		t.Fatalf("layer heights: %+v", r.Layers)
	}
	kinds := map[toolpath.Kind]int{}
	for _, s := range r.Segments {
		kinds[s.Kind]++
	}
	if kinds[toolpath.ExternalPerimeter] != 2 || kinds[toolpath.Infill] != 1 || kinds[toolpath.Support] != 1 || kinds[toolpath.Other] < 2 {
		t.Fatalf("kinds: %v", kinds)
	}
	if !r.Supports {
		t.Fatal("support segments must set Supports")
	}
	found := false
	for _, d := range r.Diagnostics {
		if d.Code == "unknown_type" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an unrecognized ;TYPE: must be reported: %+v", r.Diagnostics)
	}
}

func TestLayersFromZWhenNoMarkers(t *testing.T) {
	r := parse(t, "M83\nG1 X0 Y0 Z0.2\nG1 X10 E1\nG1 X10 Y10 E1\nG1 Z0.4\nG1 X0 E1\nG1 Z0.6\nG1 X10 E1\n")
	if len(r.Layers) != 3 {
		t.Fatalf("layers: %+v", r.Layers)
	}
}

func TestCuraLayerMarkers(t *testing.T) {
	src := "M83\n;LAYER:0\nG1 X0 Y0 Z0.3\n;TYPE:WALL-OUTER\nG1 X10 E1\n;LAYER:1\nG1 Z0.5\n;TYPE:SKIN\nG1 X0 E1\n"
	r := parse(t, src)
	if len(r.Layers) != 2 || r.Segments[1].Kind != toolpath.ExternalPerimeter {
		t.Fatalf("cura: layers=%+v seg1=%+v", r.Layers, r.Segments[1])
	}
	if r.Segments[len(r.Segments)-1].Kind != toolpath.TopBottom {
		t.Fatalf("SKIN must map to top/bottom")
	}
}

func TestHarmlessCommandsAreReportedNotSilent(t *testing.T) {
	r := parse(t, "M104 S210\nM140 S60\nM106 S255\nM900 K0.05\nM9999 foo\nG1 X1 Y1 Z0.2\nG1 X2 E1\n")
	counts := map[string]toolpath.Level{}
	for _, d := range r.Diagnostics {
		counts[d.Code] = d.Level
	}
	for _, c := range []string{"ignored_m104", "ignored_m140", "ignored_m106", "ignored_m900"} {
		if counts[c] != toolpath.LevelInfo {
			t.Errorf("missing info diagnostic %s in %v", c, counts)
		}
	}
	if counts["unknown_m9999"] != toolpath.LevelWarning {
		t.Errorf("unknown M command must warn: %v", counts)
	}
	// errors and warnings sort before info
	if r.Diagnostics[0].Level == toolpath.LevelInfo {
		t.Errorf("diagnostics must be ordered by severity: %+v", r.Diagnostics)
	}
}

func TestUnsupportedCommandsFail(t *testing.T) {
	for _, c := range []string{"G20", "G18", "G5 X1 Y1", "G54", "G38.2 Z-5", "G99 X1", "G3 X1 Y1 R5", "X10 Y10", "G1 Xabc", "G1 X999999999", "G1 X1 F-5", "T999"} {
		err := parseErr(t, "G1 X0 Y0 Z0.2\n"+c+"\n")
		var ge *Error
		if !errors.As(err, &ge) || ge.Line != 2 {
			t.Errorf("%q: want *Error on line 2, got %v", c, err)
		}
	}
}

func TestNoMovesIsAnError(t *testing.T) {
	parseErr(t, "M104 S200\n; nothing\n")
	parseErr(t, "")
}

func TestHomingAndToolChange(t *testing.T) {
	r := parse(t, "G1 X50 Y50 Z5\nG28\nT1\nG1 X1 Y1 E0.1 Z0.2\n")
	last := r.Segments[len(r.Segments)-1]
	if last.Tool != 1 {
		t.Fatalf("tool: %+v", last)
	}
	if last.X0 != 0 || last.Y0 != 0 { // homing reset the position without a visible move
		t.Fatalf("after G28 the nozzle starts at the origin: %+v", last)
	}
}

func TestLineNumbersChecksumsAndCase(t *testing.T) {
	r := parse(t, "N1 g1 x5 y5 z0.2*12\nN2 G1 X10 E1 ; comment\n")
	if len(r.Segments) != 2 || r.Segments[1].X1 != 10 {
		t.Fatalf("%+v", r.Segments)
	}
}

func TestLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString("M83\nG1 X0 Y0 Z0.2\n")
	for i := 0; i < 50; i++ {
		b.WriteString("G1 X1 Y1 E1\nG1 X0 Y0 E1\n")
	}
	if _, err := Parse(context.Background(), strings.NewReader(b.String()), Options{MaxSegments: 20}); err == nil || !strings.Contains(err.Error(), "segments") {
		t.Fatalf("segment limit: %v", err)
	}
	if _, err := Parse(context.Background(), strings.NewReader(b.String()), Options{MaxBytes: 100}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("size limit: %v", err)
	}
	if _, err := Parse(context.Background(), strings.NewReader("G1 X"+strings.Repeat("1", 2<<20)), Options{}); err == nil {
		t.Fatal("a 2 MiB line must be rejected")
	}
}

func TestCancellation(t *testing.T) {
	var b strings.Builder
	b.WriteString("M83\nG1 X0 Y0 Z0.2\n")
	for i := 0; i < 20000; i++ {
		b.WriteString("G1 X1 Y1 E1\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Parse(ctx, strings.NewReader(b.String()), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestDeterministicTimes(t *testing.T) {
	src := "M83\nG1 X0 Y0 Z0.2 F6000\nG1 X60 E2 F1200\nG1 E-1 F1800\nG1 X0 F6000\n"
	a, b := parse(t, src), parse(t, src)
	if a.Duration != b.Duration {
		t.Fatal("durations differ between runs")
	}
	// z lift .2mm@100 + 60mm@20mm/s=3s + retract 1mm@30=.033 + 60mm@100 = .6
	if want := 0.002 + 3.0 + 1.0/30 + 0.6; !near(a.Duration, want) {
		t.Fatalf("duration %v, want %v", a.Duration, want)
	}
}

func TestIgnoredCommandsWithOddParametersDoNotFailTheParse(t *testing.T) {
	r := parse(t, "M862.3 P \"MK3S\"\nM486 S0 A\"cube\"\nM115 U3.9.3\nM117 Hello world\nG1 X1 Y1 Z0.2\nG1 X2 E1\n")
	if len(r.Segments) != 2 {
		t.Fatalf("segments %d", len(r.Segments))
	}
	n := 0
	for _, d := range r.Diagnostics {
		if d.Level == toolpath.LevelInfo {
			n++
		}
	}
	if n < 3 {
		t.Fatalf("each ignored command must still be reported: %+v", r.Diagnostics)
	}
}

func TestDiagnosticsAreBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("G1 X1 Y1 Z0.2\nG1 X2 E1\n")
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, "M%d\n", 2000+i)
	}
	r := parse(t, b.String())
	if len(r.Diagnostics) > maxDiagnostics+1 {
		t.Fatalf("%d diagnostics kept", len(r.Diagnostics))
	}
}

func TestTrailingTravelOnlyLayerIsFolded(t *testing.T) {
	r := parse(t, "M83\n;LAYER_CHANGE\nG1 X0 Y0 Z0.2\nG1 X10 E1\n;LAYER_CHANGE\nG1 Z5\nG1 X5\n")
	if len(r.Layers) != 1 || r.Layers[0].Count != len(r.Segments) {
		t.Fatalf("%+v", r.Layers)
	}
}

func TestRelativeExtrusionWithPerLayerG92(t *testing.T) {
	// what PrusaSlicer emits with use_relative_e_distances and layer_gcode = G92 E0
	r := parse(t, "M83\nG92 E0\n;LAYER_CHANGE\nG92 E0\nG1 X0 Y0 Z0.2\nG1 X10 E0.5\n;LAYER_CHANGE\nG92 E0\nG1 Z0.4\nG1 X0 E0.5\n")
	if len(r.Layers) != 2 || !near(r.FilamentMM, 1) {
		t.Fatalf("layers %d filament %v", len(r.Layers), r.FilamentMM)
	}
}
