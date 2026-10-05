package selectors

import "testing"

func TestParseAndCanonical(t *testing.T) {
	s, err := Parse(" face( side=+Z , feature=base_plate ) ")
	if err != nil {
		t.Fatal(err)
	}
	if s.String() != "face(feature=base_plate,side=+Z)" || s.Feature() != "base_plate" || FeatureOf("body(feature=x)") != "x" {
		t.Fatalf("got %s", s.String())
	}
	for _, bad := range []string{"", "face", "face()", "face(side=+Z)", "blob(feature=a)", "face(feature=a,feature=b)", "face(feature=)", "face(feature=a,side)"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
	if FeatureOf("junk") != "" {
		t.Error("FeatureOf(junk)")
	}
}

func TestResolveFaces(t *testing.T) {
	b := Bounds{Min: [3]float64{0, 0, 0}, Max: [3]float64{100, 60, 4}}
	for side, want := range map[string]struct {
		n    [3]float64
		c    [3]float64
		area float64
	}{
		"+Z": {[3]float64{0, 0, 1}, [3]float64{50, 30, 4}, 6000},
		"-Z": {[3]float64{0, 0, -1}, [3]float64{50, 30, 0}, 6000},
		"+X": {[3]float64{1, 0, 0}, [3]float64{100, 30, 2}, 240},
		"-Y": {[3]float64{0, -1, 0}, [3]float64{50, 0, 2}, 400},
	} {
		sel, _ := Parse("face(feature=p,side=" + side + ")")
		d, err := Resolve(sel, b)
		if err != nil {
			t.Fatal(err)
		}
		if *d.Normal != want.n || d.Centroid != want.c || d.Area != want.area || d.SurfaceType != "plane" {
			t.Errorf("%s: %+v", side, d)
		}
	}
	sel, _ := Parse("face(feature=p,side=up)")
	if _, err := Resolve(sel, b); err == nil {
		t.Error("bad side")
	}
	sel, _ = Parse("edges(feature=p,kind=external)")
	if _, err := Resolve(sel, b); err == nil {
		t.Error("edges are Phase 2")
	}
}
