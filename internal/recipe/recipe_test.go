package recipe

import (
	"math"
	"strings"
	"testing"
)

func valid() map[string]any {
	return map[string]any{
		"type":   "stamp",
		"base":   map[string]any{"shape": "rectangle", "width": 60.0, "height": 40.0, "thickness": 4.0},
		"design": map[string]any{"source": map[string]any{"svg": "<svg/>"}, "relief_height": 2.0, "margin": 3.0},
		"mirror": true,
	}
}

func TestParseStampFillsDefaultsAndReportsThem(t *testing.T) {
	m := valid()
	delete(m["base"].(map[string]any), "thickness")
	delete(m["design"].(map[string]any), "relief_height")
	delete(m["design"].(map[string]any), "margin")
	s, assumed, err := ParseStamp(m)
	if err != nil {
		t.Fatal(err)
	}
	if s.Base.Thickness != DefaultBaseThickness || s.Design.ReliefHeight != DefaultReliefHeight || s.Design.Margin != DefaultMargin {
		t.Fatalf("defaults: %+v", s)
	}
	if len(assumed) != 3 {
		t.Fatalf("assumptions must be listed: %v", assumed)
	}
	// nothing is assumed when everything is given
	if _, assumed, err := ParseStamp(valid()); err != nil || len(assumed) != 0 {
		t.Fatalf("%v %v", err, assumed)
	}
}

func TestShrinkageIsNeverHardcoded(t *testing.T) {
	s, _, err := ParseStamp(valid())
	if err != nil {
		t.Fatal(err)
	}
	if s.Shrinkage != 0 || s.ScaleFactor() != 1 {
		t.Fatalf("no shrinkage unless the user gives one: %v", s.Shrinkage)
	}
	m := valid()
	m["shrinkage_percent"] = 12.0
	s, _, _ = ParseStamp(m)
	if math.Abs(s.ScaleFactor()-1/0.88) > 1e-12 {
		t.Fatalf("factor %v", s.ScaleFactor())
	}
	for _, bad := range []float64{-1, 40, 99} {
		m["shrinkage_percent"] = bad
		if _, _, err := ParseStamp(m); err == nil {
			t.Errorf("shrinkage %v accepted", bad)
		}
	}
}

func TestParseStampErrorsAreActionable(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(m map[string]any)
		wants string
	}{
		{"no size", func(m map[string]any) { delete(m["base"].(map[string]any), "width") }, "stamp size"},
		{"wrong type", func(m map[string]any) { m["type"] = "tube" }, `must be "stamp"`},
		{"two sources", func(m map[string]any) {
			m["design"].(map[string]any)["source"] = map[string]any{"svg": "x", "path": "y"}
		}, "exactly one"},
		{"no source", func(m map[string]any) { m["design"].(map[string]any)["source"] = map[string]any{} }, "exactly one"},
		{"unknown field", func(m map[string]any) { m["design"].(map[string]any)["depth"] = 1.0 }, `unknown field "depth"`},
		{"bad mode", func(m map[string]any) { m["design"].(map[string]any)["mode"] = "blobs" }, "filled_regions or centerline"},
		{"margin too big", func(m map[string]any) { m["design"].(map[string]any)["margin"] = 25.0 }, "leaves no room"},
		{"fillet too big", func(m map[string]any) {
			m["base"].(map[string]any)["rounded_edges"] = true
			m["base"].(map[string]any)["edge_radius"] = 3.0
		}, "too large"},
		{"handle too wide", func(m map[string]any) { m["handle"] = map[string]any{"enabled": true, "diameter": 50.0} }, "does not fit"},
		{"sketch centerline", func(m map[string]any) {
			m["design"].(map[string]any)["source"] = map[string]any{"sketch": "s"}
			m["design"].(map[string]any)["mode"] = "centerline"
		}, "use filled_regions"},
		{"negative height", func(m map[string]any) { m["design"].(map[string]any)["relief_height"] = -1.0 }, "positive"},
		{"bad shape", func(m map[string]any) { m["base"].(map[string]any)["shape"] = "hexagon" }, "rectangle or circle"},
	}
	for _, c := range cases {
		m := valid()
		c.edit(m)
		_, _, err := ParseStamp(m)
		if err == nil || !strings.Contains(err.Error(), c.wants) {
			t.Errorf("%s: %v (want %q)", c.name, err, c.wants)
		}
	}
}

func TestCircularBaseHandleAndRoundedEdges(t *testing.T) {
	m := map[string]any{"type": "stamp",
		"base":   map[string]any{"shape": "circle", "diameter": 50.0, "thickness": 5.0, "rounded_edges": true},
		"design": map[string]any{"source": map[string]any{"sketch": "logo"}, "relief_height": 1.0, "margin": 4.0},
		"handle": map[string]any{"enabled": true}}
	s, assumed, err := ParseStamp(m)
	if err != nil {
		t.Fatal(err)
	}
	if s.Base.Shape != "circle" || s.Base.MinSide() != 50 || s.Base.EdgeRadius <= 0 || s.Base.EdgeRadius > 1 {
		t.Fatalf("%+v", s.Base)
	}
	if s.Handle.Diameter != 20 || s.Handle.Height != DefaultHandleHeight {
		t.Fatalf("handle %+v", s.Handle)
	}
	joined := strings.Join(assumed, ";")
	if !strings.Contains(joined, "handle height") {
		t.Fatalf("assumptions %v", assumed)
	}
}

func TestMapRoundTripAndMerge(t *testing.T) {
	s, _, _ := ParseStamp(valid())
	canon := s.Map()
	again, assumed, err := ParseStamp(canon)
	if err != nil || len(assumed) != 0 {
		t.Fatalf("canonical form must parse without assumptions: %v %v", err, assumed)
	}
	if again.Base.Width != 60 || !again.Mirror {
		t.Fatalf("%+v", again)
	}
	merged := Merge(canon, map[string]any{
		"base":   map[string]any{"thickness": 6.0},
		"design": map[string]any{"relief_height": 1.0},
		"handle": map[string]any{"enabled": true, "height": 20.0},
		"mirror": nil,
	})
	out, _, err := ParseStamp(merged)
	if err != nil {
		t.Fatal(err)
	}
	if out.Base.Thickness != 6 || out.Design.ReliefHeight != 1 || !out.Handle.Enabled || out.Handle.Height != 20 || out.Mirror {
		t.Fatalf("%+v", out)
	}
	if out.Base.Width != 60 {
		t.Fatal("merge must keep untouched fields")
	}
	if s.Base.Thickness != 4 {
		t.Fatal("merge must not mutate its input")
	}
}
