package workspace

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLinearConversion(t *testing.T) {
	for _, c := range []struct {
		unit string
		mm   float64
		want float64
	}{
		{MM, 100, 100}, {CM, 100, 10}, {M, 2500, 2.5}, {Inch, 25.4, 1}, {Foot, 304.8, 1}, {Inch, 100, 3.937007874015748},
	} {
		got, err := FromMM(c.mm, c.unit)
		if err != nil || !near(got, c.want) {
			t.Fatalf("FromMM(%v, %s) = %v, %v; want %v", c.mm, c.unit, got, err, c.want)
		}
		back, err := ToMM(got, c.unit)
		if err != nil || !near(back, c.mm) {
			t.Fatalf("ToMM round trip %s: %v", c.unit, back)
		}
	}
	if v, err := ConvertLength(1, Foot, Inch); err != nil || !near(v, 12) {
		t.Fatalf("1 ft = %v in", v)
	}
	if _, err := ToMM(1, "furlong"); err == nil {
		t.Fatal("accepted an unknown unit")
	}
}

func TestAngularConversionAndNormalization(t *testing.T) {
	r, _ := ToRadians(180, Deg)
	if !near(r, math.Pi) {
		t.Fatalf("180° = %v rad", r)
	}
	d, _ := FromRadians(math.Pi/4, Deg)
	if !near(d, 45) {
		t.Fatalf("π/4 = %v°", d)
	}
	if v, _ := ToRadians(1.5, Rad); v != 1.5 {
		t.Fatal("radians must pass through")
	}
	if _, err := ToRadians(1, "grad"); err == nil {
		t.Fatal("accepted an unknown angle unit")
	}
	for _, c := range []struct{ in, want float64 }{
		{0, 0}, {math.Pi, math.Pi}, {-math.Pi, math.Pi}, {3 * math.Pi, math.Pi}, {2 * math.Pi, 0},
		{-math.Pi / 2, -math.Pi / 2}, {5 * math.Pi / 2, math.Pi / 2}, {-5 * math.Pi / 2, -math.Pi / 2},
	} {
		if got := NormalizeAngle(c.in); !near(got, c.want) {
			t.Fatalf("NormalizeAngle(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormattingAndPrecision(t *testing.T) {
	u := Defaults().Units
	if s := u.FormatLength(100); s != "100.00 mm" {
		t.Fatalf("default length: %q", s)
	}
	if s := u.FormatAngle(math.Pi / 4); s != "45.0°" {
		t.Fatalf("default angle: %q", s)
	}
	u.Linear, u.LinearPrecision = Inch, 2
	if s := u.FormatLength(100); s != "3.94 in" {
		t.Fatalf("inches: %q", s)
	}
	u.Linear, u.LinearPrecision = CM, 0
	if s := u.FormatLength(104); s != "10 cm" {
		t.Fatalf("cm, 0 decimals: %q", s)
	}
	u.Linear, u.LinearPrecision = MM, 1
	if s := u.FormatLength(-0.01); s != "0.0 mm" {
		t.Fatalf("negative zero: %q", s)
	}
	u.Angular, u.AngularPrecision = Rad, 3
	if s := u.FormatAngle(math.Pi); s != "3.142 rad" {
		t.Fatalf("radians: %q", s)
	}
	u.Linear = Inch
	if mm, _ := u.LengthToMM(2); !near(mm, 50.8) {
		t.Fatalf("typed 2 in = %v mm", mm)
	}
}

// Display units are a view of the canonical value: switching them changes what is shown and
// how input is read, never the stored millimetres.
func TestSwitchingUnitsKeepsCanonicalValues(t *testing.T) {
	s := Defaults()
	position := 100.0 // mm, owned by the geometry
	shown := map[string]string{}
	for _, unit := range LinearUnits {
		s.Units.Linear = unit
		shown[unit] = s.Units.FormatLength(position)
	}
	if position != 100 || shown[MM] != "100.00 mm" || shown[CM] != "10.00 cm" || shown[Inch] != "3.94 in" {
		t.Fatalf("shown %v, position %v", shown, position)
	}
	// Grid settings are canonical too: changing display units leaves them alone.
	before := s.Grid
	s.Units.Linear = Foot
	if s.Grid != before {
		t.Fatal("display units changed the grid")
	}
}

func TestValidation(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	neg := -5.0
	zero := 0.0
	for name, mut := range map[string]func(*Settings){
		"zero spacing":       func(s *Settings) { s.Grid.Spacing = 0 },
		"negative spacing":   func(s *Settings) { s.Grid.Spacing = -1 },
		"NaN spacing":        func(s *Settings) { s.Grid.Spacing = math.NaN() },
		"zero extent":        func(s *Settings) { s.Grid.Extent = 0 },
		"too many lines":     func(s *Settings) { s.Grid.Spacing, s.Grid.Extent = 0.01, 1000 },
		"plane":              func(s *Settings) { s.Grid.Plane = "XW" },
		"major interval":     func(s *Settings) { s.Grid.MajorInterval = 0 },
		"origin":             func(s *Settings) { s.Grid.Origin[1] = math.Inf(1) },
		"rotation increment": func(s *Settings) { s.Snap.RotationIncrement = 0 },
		"rotation over 360":  func(s *Settings) { s.Snap.RotationIncrement = 400 },
		"negative step":      func(s *Settings) { s.Snap.TranslationIncrement = &neg },
		"zero step":          func(s *Settings) { s.Snap.TranslationIncrement = &zero },
		"linear unit":        func(s *Settings) { s.Units.Linear = "yd" },
		"angular unit":       func(s *Settings) { s.Units.Angular = "grad" },
		"precision":          func(s *Settings) { s.Units.LinearPrecision = 9 },
		"negative precision": func(s *Settings) { s.Units.AngularPrecision = -1 },
	} {
		s := Defaults()
		mut(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := Defaults()
	s.Grid.Plane, s.Units.Linear = "xz", "IN"
	if err := s.Normalized().Validate(); err != nil {
		t.Fatalf("normalized spelling rejected: %v", err)
	}
}

func TestGridCoordinateTransforms(t *testing.T) {
	g := Defaults().Grid
	g.Origin = [3]float64{10, 20, 30}
	for _, plane := range Planes {
		g.Plane = plane
		p := [3]float64{-7.5, 42, 3.25}
		q := g.WorldToGrid(p)
		if back := g.GridToWorld(q); back != p {
			t.Fatalf("%s round trip: %v → %v → %v", plane, p, q, back)
		}
	}
	g.Plane = PlaneXZ
	if q := g.WorldToGrid([3]float64{15, 25, 35}); q != [3]float64{5, 5, 5} {
		t.Fatalf("XZ offsets: %v", q)
	}
	g.Origin = [3]float64{}
	if q := g.WorldToGrid([3]float64{1, 2, 3}); q != [3]float64{1, 3, 2} {
		t.Fatalf("XZ: u=X, v=Z, w=Y; got %v", q)
	}
	g.Plane = PlaneYZ
	if q := g.WorldToGrid([3]float64{1, 2, 3}); q != [3]float64{2, 3, 1} {
		t.Fatalf("YZ: u=Y, v=Z, w=X; got %v", q)
	}
}

func snapOn() Settings {
	s := Defaults()
	s.Snap.Enabled = true
	return s
}

func TestGridSnappingPositiveAndNegative(t *testing.T) {
	c := Context{Settings: snapOn()}
	for _, k := range []struct{ in, want [3]float64 }{
		{[3]float64{14, 16, 3.3}, [3]float64{10, 20, 3.3}}, // in-plane only: Z is kept
		{[3]float64{-14, -16, -3}, [3]float64{-10, -20, -3}},
		{[3]float64{15, -15, 0}, [3]float64{20, -20, 0}}, // halves go away from zero, symmetrically
		{[3]float64{-0.4, 0.4, 0}, [3]float64{0, 0, 0}},
	} {
		if got, ok := SnapPosition(k.in, c); !ok || got != k.want {
			t.Fatalf("SnapPosition(%v) = %v, %v; want %v", k.in, got, ok, k.want)
		}
	}
	// The grid origin and plane are respected.
	c.Settings.Grid.Origin = [3]float64{3, 0, 5}
	c.Settings.Grid.Plane = PlaneXZ
	if got, _ := SnapPosition([3]float64{11, 7.7, 19}, c); got != [3]float64{13, 7.7, 15} {
		t.Fatalf("XZ grid at (3,0,5): %v", got)
	}
}

func TestSnappingIsDeterministicWithoutFloatDrift(t *testing.T) {
	s := snapOn()
	s.Grid.Spacing = 0.1
	c := Context{Settings: s}
	if got, _ := SnapPosition([3]float64{0.29, 0.35, 0}, c); got != [3]float64{0.3, 0.4, 0} {
		t.Fatalf("0.1 mm grid: %v", got)
	}
	// Snapping a snapped value is the identity, however often it is repeated.
	p := [3]float64{123.456, -98.765, 1}
	first, _ := SnapPosition(p, c)
	cur := first
	for i := 0; i < 1000; i++ {
		cur, _ = SnapPosition(cur, c)
	}
	if cur != first {
		t.Fatalf("drift: %v vs %v", cur, first)
	}
	// Accumulating snapped steps lands on exact multiples.
	d := [3]float64{}
	for i := 0; i < 30; i++ {
		step, _ := SnapTranslation([3]float64{0.1, 0, 0}, c)
		d[0] += step[0]
		d, _ = SnapTranslation(d, c)
	}
	if d[0] != 3 {
		t.Fatalf("30 snapped 0.1 mm steps = %v", d[0])
	}
}

func TestTranslationSnapping(t *testing.T) {
	c := Context{Settings: snapOn()}
	if got, ok := SnapTranslation([3]float64{14, -16, 4.9}, c); !ok || got != [3]float64{10, -20, 0} {
		t.Fatalf("grid-spacing increment: %v", got)
	}
	inc := 2.5
	c.Settings.Snap.TranslationIncrement = &inc
	c.Settings.Grid.Origin = [3]float64{1, 1, 1} // a displacement ignores the grid origin
	if got, _ := SnapTranslation([3]float64{3.7, -3.8, 0.1}, c); got != [3]float64{2.5, -5, 0} {
		t.Fatalf("2.5 mm increment: %v", got)
	}
}

func TestRotationSnapping(t *testing.T) {
	s := snapOn()
	s.Snap.Rotation = true
	c := Context{Settings: s}
	deg := func(d float64) float64 { return d * math.Pi / 180 }
	for _, k := range []struct{ in, want float64 }{{7, 0}, {8, 15}, {-8, -15}, {44, 45}, {-187, -180}, {361, 360}} {
		got, ok := SnapAngle(deg(k.in), c)
		if !ok || !near(got, deg(k.want)) {
			t.Fatalf("SnapAngle(%v°) = %v°", k.in, got*180/math.Pi)
		}
	}
	c.Settings.Snap.RotationIncrement = 45
	if got, _ := SnapAngle(deg(100), c); got != deg(90) {
		t.Fatalf("45° steps: %v", got*180/math.Pi)
	}
}

func TestSnapFlagsAreIndependent(t *testing.T) {
	p, d, a := [3]float64{14, 14, 0}, [3]float64{14, 0, 0}, 0.3
	check := func(name string, s Settings, pos, tr, rot bool) {
		t.Helper()
		c := Context{Settings: s}
		if _, ok := SnapPosition(p, c); ok != pos {
			t.Errorf("%s: position snap %v", name, ok)
		}
		if _, ok := SnapTranslation(d, c); ok != tr {
			t.Errorf("%s: translation snap %v", name, ok)
		}
		if _, ok := SnapAngle(a, c); ok != rot {
			t.Errorf("%s: rotation snap %v", name, ok)
		}
	}
	s := Defaults()
	check("defaults (snapping off)", s, false, false, false)
	s.Snap.Enabled = true
	check("enabled", s, true, true, false)
	s.Snap.Rotation = true
	check("rotation on", s, true, true, true)
	s.Snap.Grid = false
	check("grid off", s, false, true, true)
	s.Snap.Translation = false
	check("translation off", s, false, false, true)
	s.Snap.Grid, s.Grid.Enabled = true, false
	check("grid disabled", s, false, false, true)
	s.Snap.Enabled = false
	check("global off", s, false, false, false)
	s = snapOn()
	if _, ok := SnapPosition(p, Context{Settings: s, Bypass: true}); ok {
		t.Error("bypass did not suspend snapping")
	}
	if got, _ := SnapPosition(p, Context{Settings: Defaults()}); got != p {
		t.Error("inactive snap changed the position")
	}
}

func TestSchemaAndSafeDecoding(t *testing.T) {
	b, err := json.Marshal(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"schema_version":1`, `"translation_increment":null`, `"origin":[0,0,0]`, `"linear":"mm"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("serialized defaults lack %s: %s", k, b)
		}
	}
	got, warns, err := Decode(b, Defaults())
	if err != nil || len(warns) > 0 || !got.Equal(Defaults()) {
		t.Fatalf("round trip: %+v %v %v", got, warns, err)
	}
	// Missing fields keep the base, unknown ones are ignored, spelling is normalized.
	got, warns, err = Decode([]byte(`{"units":{"linear":"IN"},"grid":{"plane":"xz","spacing":5,"future":true},"later":{}}`), Defaults())
	if err != nil || len(warns) > 0 {
		t.Fatalf("partial: %v %v", warns, err)
	}
	if got.Units.Linear != Inch || got.Units.LinearPrecision != 2 || got.Grid.Plane != PlaneXZ || got.Grid.Spacing != 5 || got.Grid.Extent != 1000 {
		t.Fatalf("partial decode: %+v", got)
	}
	// A bad section falls back on its own; the others survive.
	got, warns, err = Decode([]byte(`{"units":{"linear":"cm"},"grid":{"spacing":-1},"snap":{"enabled":"yes"}}`), Defaults())
	if err != nil || len(warns) != 2 || got.Units.Linear != CM || got.Grid != Defaults().Grid || got.Snap.Enabled {
		t.Fatalf("bad sections: %+v %v %v", got, warns, err)
	}
	// Newer schema: read what is known, say so.
	if _, warns, _ = Decode([]byte(`{"schema_version":9,"units":{"linear":"m"}}`), Defaults()); len(warns) != 1 {
		t.Fatalf("newer schema warnings: %v", warns)
	}
	if _, _, err = Decode([]byte(`[1,2]`), Defaults()); err == nil {
		t.Fatal("accepted a non-object")
	}
	// An explicit null increment means "inherit the grid spacing".
	inc := 4.0
	base := Defaults()
	base.Snap.TranslationIncrement = &inc
	got, _, _ = Decode([]byte(`{"snap":{"translation_increment":null}}`), base)
	if got.Snap.TranslationIncrement != nil || got.TranslationStep() != 10 {
		t.Fatalf("null increment: %v", got.Snap.TranslationIncrement)
	}
}
