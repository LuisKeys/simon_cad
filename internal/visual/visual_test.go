package visual

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"
)

// boxMesh is an axis-aligned box as 12 triangles.
func boxMesh(id string, min, max [3]float64) Mesh {
	var pos []float32
	for i := 0; i < 8; i++ {
		x, y, z := min[0], min[1], min[2]
		if i&1 != 0 {
			x = max[0]
		}
		if i&2 != 0 {
			y = max[1]
		}
		if i&4 != 0 {
			z = max[2]
		}
		pos = append(pos, float32(x), float32(y), float32(z))
	}
	idx := []uint32{0, 2, 1, 1, 2, 3, 4, 5, 6, 5, 7, 6, 0, 1, 4, 1, 5, 4, 2, 6, 3, 3, 6, 7, 0, 4, 2, 2, 4, 6, 1, 3, 5, 3, 7, 5}
	return Mesh{ID: id, Positions: pos, Indices: idx}
}

func TestRenderedSilhouetteMatchesTheGeometry(t *testing.T) {
	m := boxMesh("body", [3]float64{0, 0, 0}, [3]float64{100, 40, 30})
	frame := FrameFor([3]float64{0, 0, 0}, [3]float64{100, 40, 30})
	for view, want := range map[string]float64{"left": 30.0 / 100, "front": 30.0 / 40, "top": 40.0 / 100} {
		cam, _ := StandardCamera(view)
		im, err := Rasterize([]Mesh{m}, cam, frame, Options{Width: 400, Height: 400, Mode: ModeSilhouette})
		if err != nil {
			t.Fatal(err)
		}
		if got := aspect(im.Mask); math.Abs(got-want) > 0.02 {
			t.Errorf("%s: silhouette aspect %.3f, want %.3f", view, got, want)
		}
		if c := im.RGBA.RGBAAt(200, 200); c.R != 255 {
			t.Errorf("%s: the centre must be object-white in silhouette mode: %v", view, c)
		}
		if c := im.RGBA.RGBAAt(1, 1); c.R != 0 {
			t.Errorf("%s: background must be black: %v", view, c)
		}
	}
	// the same frame gives the same pixels: renders of successive iterations are comparable
	cam, _ := StandardCamera("front_3q")
	a, _ := Rasterize([]Mesh{m}, cam, frame, Options{Width: 128, Height: 128})
	b, _ := Rasterize([]Mesh{m}, cam, frame, Options{Width: 128, Height: 128})
	if !bytes.Equal(a.RGBA.Pix, b.RGBA.Pix) || a.Mask.Count() == 0 {
		t.Fatal("rendering must be deterministic")
	}
	if _, err := Rasterize([]Mesh{m}, cam, frame, Options{Width: 10, Height: 10, Mode: "wire"}); err == nil {
		t.Fatal("unknown modes are refused")
	}
}

// rectPNG draws a dark w×h rectangle (plus an optional bump on top) on a light background.
func rectPNG(t *testing.T, iw, ih int, x0, y0, x1, y1 int, bump bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, iw, ih))
	for y := 0; y < ih; y++ {
		for x := 0; x < iw; x++ {
			c := color.RGBA{245, 244, 240, 255}
			in := x >= x0 && x < x1 && y >= y0 && y < y1
			if bump && x >= (x0+x1)/2-20 && x < (x0+x1)/2+20 && y >= y0-30 && y < y0 {
				in = true
			}
			if in {
				c = color.RGBA{40, 50, 160, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestSegmentationAndSilhouetteScores(t *testing.T) {
	ref, err := Segment(rectPNG(t, 300, 200, 50, 80, 250, 140, false))
	if err != nil {
		t.Fatal(err)
	}
	if got := aspect(ref); math.Abs(got-0.3) > 0.02 {
		t.Fatalf("segmented aspect %.3f, want 0.3", got)
	}
	// a rendered box of the same proportions at a different scale and position matches
	m := boxMesh("b", [3]float64{0, 0, 0}, [3]float64{200, 50, 60})
	cam, _ := StandardCamera("left")
	im, _ := Rasterize([]Mesh{m}, cam, FrameFor([3]float64{0, 0, 0}, [3]float64{200, 50, 60}), Options{Width: 256, Height: 256, Mode: ModeSilhouette})
	same := CompareSilhouettes(ref, im.Mask, true)
	if same.IoU < 0.95 || same.Contour < 0.95 || same.BBox < 0.97 || same.Score < 0.95 {
		t.Fatalf("matching proportions: %+v", same)
	}
	// a box twice as tall is clearly worse
	tall := boxMesh("b", [3]float64{0, 0, 0}, [3]float64{200, 50, 120})
	im2, _ := Rasterize([]Mesh{tall}, cam, FrameFor([3]float64{0, 0, 0}, [3]float64{200, 50, 120}), Options{Width: 256, Height: 256, Mode: ModeSilhouette})
	worse := CompareSilhouettes(ref, im2.Mask, true)
	if worse.Score > same.Score-0.2 || worse.BBox > 0.6 {
		t.Fatalf("wrong height must score lower: %+v vs %+v", worse, same)
	}
	// an asymmetric shape facing the other way is recognized through mirroring
	bumpRef, _ := Segment(rectPNG(t, 300, 200, 50, 80, 250, 140, true))
	if s := CompareSilhouettes(bumpRef, bumpRef.Mirror(), true); !(s.IoU > 0.99) {
		t.Fatalf("mirrored self-match: %+v", s)
	}
	if _, err := DifferencePNG(ref, im2.Mask, worse.Mirrored); err != nil {
		t.Fatal(err)
	}
	// a blank image has no silhouette
	if _, err := Segment(rectPNG(t, 100, 100, 0, 0, 0, 0, false)); err == nil {
		t.Fatal("a blank reference must be refused")
	}
}

func TestRatiosAndScoring(t *testing.T) {
	// side view 1000x400 px: length 800 px, wheelbase 480 px, height 200 px
	side := Landmarks{
		"front_tip": {0.9, 0.5}, "rear_tip": {0.1, 0.5},
		"front_wheel_center": {0.74, 0.85}, "rear_wheel_center": {0.26, 0.85}, "wheel_radius": {0.05, 0},
		"roof_max": {0.5, 0.4}, "ground": {0.5, 0.9}, "windshield_lower": {0.65, 0.6}, "windshield_upper": {0.55, 0.42}, "roof_rear": {0.35, 0.42},
	}
	r := Ratios(side, 1000, 400, Landmarks{"body_left": {0.1, 0.5}, "body_right": {0.9, 0.5}, "roof_left": {0.3, 0.2}, "roof_right": {0.7, 0.2}})
	for k, want := range map[string]float64{"wheelbase_ratio": 0.6, "height_length_ratio": 0.25, "front_overhang_ratio": 0.2,
		"rear_overhang_ratio": 0.2, "cabin_length_ratio": 0.375, "wheel_diameter_height_ratio": 0.5, "windshield_height_ratio": 0.36, "roof_width_ratio": 0.5} {
		if math.Abs(r[k]-want) > 0.002 {
			t.Errorf("%s = %v, want %v", k, r[k], want)
		}
	}
	score, per := CompareRatios(map[string]float64{"wheelbase_ratio": 0.6, "height_length_ratio": 0.25}, map[string]float64{"wheelbase_ratio": 0.6, "height_length_ratio": 0.30})
	if per["wheelbase_ratio"] != 1 || per["height_length_ratio"] != 0.8 || score != 0.9 {
		t.Fatalf("ratio scores: %v %v", score, per)
	}
	box := [4]float64{0.1, 0.4, 0.9, 0.9}
	if s, ok := LandmarkAlignment(side, box, side, box, false, "wheel"); !ok || s != 1 {
		t.Fatalf("identical landmarks: %v", s)
	}
	blockout := Stages[0]
	scores := map[string]float64{MetricSide: 0.93, MetricProportion: 0.95, MetricWheels: 0.91}
	if g := blockout.Check(scores); !g.Passed || len(g.Skipped) != 1 {
		t.Fatalf("gate: %+v", g)
	}
	scores[MetricSide] = 0.8
	if g := blockout.Check(scores); g.Passed || len(g.Failing) != 1 {
		t.Fatalf("a failing silhouette must hold the stage: %+v", g)
	}
	if blockout.Check(map[string]float64{}).Passed {
		t.Fatal("a stage never passes without measurements")
	}
	if o := blockout.Overall(map[string]float64{MetricSide: 1, MetricSemantic: 0}); math.Abs(o-0.75) > 1e-9 {
		t.Fatalf("weights renormalize over measured metrics: %v", o)
	}
}

func TestRegionsAndLocks(t *testing.T) {
	s := NewState()
	if _, err := s.DefineRegion("wheels", []string{"wheel_fl", "wheel_rl"}, []string{"vehicle.wheel_radius"}); err != nil {
		t.Fatal(err)
	}
	if s.LockedObject("wheel_fl") != "" {
		t.Fatal("not locked yet")
	}
	if _, err := s.SetLocked("Wheels", true); err != nil {
		t.Fatal(err)
	}
	if s.LockedObject("wheel_fl") != "wheels" || s.LockedParameter("vehicle", "wheel_radius") != "wheels" || s.LockedParameter("vehicle", "roof_height") != "" {
		t.Fatal("lock lookups")
	}
	if _, err := s.SetLocked("roof", true); err == nil {
		t.Fatal("unknown regions cannot be locked")
	}
	s.AddReference("img_1", 800, 600)
	s.PutRender(&Render{View: "left"})
	s.AddIteration(&Iteration{Stage: "BLOCKOUT"})
	s.RecordFailed(Correction{Region: "Roof", Problem: "too high"})
	b, _ := jsonMarshal(s)
	back := NewState()
	if err := back.UnmarshalJSON(b); err != nil {
		t.Fatal(err)
	}
	if len(back.References) != 1 || !back.Regions[0].Locked || len(back.History) != 1 || !back.HasFailed(Correction{Region: "roof", Problem: "Too high"}) {
		t.Fatalf("round trip: %s", b)
	}
	if _, ok := back.Render("rnd_0001"); ok {
		t.Fatal("renders are session-only")
	}
}
