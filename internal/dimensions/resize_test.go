package dimensions

import (
	"strings"
	"testing"
)

func TestFactorsChangeOnlyTheRequestedAxis(t *testing.T) {
	f, err := Factors([3]float64{120, 80, 40}, map[int]float64{0: 150}, false)
	if err != nil {
		t.Fatal(err)
	}
	if f != [3]float64{1.25, 1, 1} {
		t.Fatalf("factors = %v", f)
	}
	if got := Resized([3]float64{120, 80, 40}, f); got != [3]float64{150, 80, 40} {
		t.Fatalf("resized = %v", got)
	}
}

func TestFactorsWithLockProportions(t *testing.T) {
	f, err := Factors([3]float64{120, 80, 40}, map[int]float64{0: 150}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := Resized([3]float64{120, 80, 40}, f); got != [3]float64{150, 100, 50} {
		t.Fatalf("proportional = %v", got)
	}
	// Several explicit dimensions are taken as given even when locked.
	f, err = Factors([3]float64{120, 80, 40}, map[int]float64{0: 60, 2: 20}, true)
	if err != nil || f != [3]float64{0.5, 1, 0.5} {
		t.Fatalf("explicit = %v %v", f, err)
	}
}

func TestFactorsRefuseBadInput(t *testing.T) {
	cases := []struct {
		cur    [3]float64
		target map[int]float64
		want   string
	}{
		{[3]float64{10, 10, 0}, map[int]float64{2: 5}, "flat"},
		{[3]float64{10, 10, 10}, map[int]float64{0: 0}, "positive"},
		{[3]float64{10, 10, 10}, map[int]float64{0: -3}, "positive"},
		{[3]float64{10, 10, 10}, map[int]float64{0: 10}, "already 10 mm"},
		{[3]float64{10, 10, 10}, map[int]float64{0: 1e6}, "at most"},
		{[3]float64{10, 10, 10}, map[int]float64{}, "at least one"},
	}
	for _, c := range cases {
		if _, err := Factors(c.cur, c.target, false); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v → %v: want an error containing %q, got %v", c.cur, c.target, c.want, err)
		}
	}
	// A flat object can still be resized along the axes it has.
	if _, err := Factors([3]float64{10, 10, 0}, map[int]float64{0: 20}, true); err != nil {
		t.Fatalf("flat sheet, proportional width: %v", err)
	}
}

func TestAxisIndex(t *testing.T) {
	for name, want := range map[string]int{"width": 0, "x": 0, "depth": 1, "y": 1, "height": 2, "z": 2} {
		if i, ok := AxisIndex(name); !ok || i != want {
			t.Fatalf("%s → %d %v", name, i, ok)
		}
	}
	if _, ok := AxisIndex("length"); ok {
		t.Fatal("length is not an axis")
	}
}
