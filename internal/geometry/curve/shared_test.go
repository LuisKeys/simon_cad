package curve

import (
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "regenerate testdata/curve_cases.json")

// The frontend (frontend/path-model.js) evaluates the same curves for its live preview. Both
// sides are tested against this file, so the preview cannot drift from the kernel's input.
const sharedFile = "../../../testdata/curve_cases.json"

type sharedPoint struct {
	P     Vec     `json:"p"`
	In    *Vec    `json:"in,omitempty"`
	Out   *Vec    `json:"out,omitempty"`
	W     float64 `json:"w,omitempty"`
	H     float64 `json:"h,omitempty"`
	Twist float64 `json:"twist,omitempty"`
}

type sharedCase struct {
	Name   string        `json:"name"`
	Type   string        `json:"type"`
	Closed bool          `json:"closed"`
	Points []sharedPoint `json:"points"`
	// expectations, produced by this package
	Length   float64     `json:"length"`
	Flat     int         `json:"flat_points_tol_0_1"`
	Samples  []sampleExp `json:"samples"`
	MinRad   *float64    `json:"min_radius,omitempty"`
	Segments int         `json:"segments"`
}

type sampleExp struct {
	T float64 `json:"t"`
	P Vec     `json:"p"`
	D Vec     `json:"tangent"`
}

func (c sharedCase) path() *Path {
	p := &Path{Type: Type(c.Type), Closed: c.Closed}
	for _, sp := range c.Points {
		p.Points = append(p.Points, Point{P: sp.P, In: sp.In, Out: sp.Out, W: sp.W, H: sp.H, Twist: sp.Twist})
	}
	return p
}

func sharedInputs() []sharedCase {
	k := 0.5522847498307936 * 10
	return []sharedCase{
		{Name: "linear-open", Type: "linear", Points: []sharedPoint{{P: Vec{0, 0, 0}}, {P: Vec{10, 0, 0}}, {P: Vec{10, 10, 5}}}},
		{Name: "bezier-circle", Type: "bezier", Closed: true, Points: []sharedPoint{
			{P: Vec{10, 0, 0}, In: &Vec{10, -k, 0}, Out: &Vec{10, k, 0}},
			{P: Vec{0, 10, 0}, In: &Vec{k, 10, 0}, Out: &Vec{-k, 10, 0}},
			{P: Vec{-10, 0, 0}, In: &Vec{-10, k, 0}, Out: &Vec{-10, -k, 0}},
			{P: Vec{0, -10, 0}, In: &Vec{-k, -10, 0}, Out: &Vec{k, -10, 0}}}},
		{Name: "bezier-open-s", Type: "bezier", Points: []sharedPoint{
			{P: Vec{0, 0, 0}, Out: &Vec{5, 0, 0}}, {P: Vec{20, 10, 3}, In: &Vec{15, 10, 3}}}},
		{Name: "catmull-open", Type: "catmull_rom", Points: []sharedPoint{
			{P: Vec{0, 0, 0}}, {P: Vec{10, 8, 2}}, {P: Vec{20, -4, 6}}, {P: Vec{35, 5, 1}}}},
		{Name: "catmull-closed", Type: "catmull_rom", Closed: true, Points: []sharedPoint{
			{P: Vec{10, 0, 0}}, {P: Vec{0, 10, 2}}, {P: Vec{-10, 0, 0}}, {P: Vec{0, -10, -2}}}},
		{Name: "bspline-open", Type: "bspline", Points: []sharedPoint{
			{P: Vec{0, 0, 0}}, {P: Vec{10, 10, 0}}, {P: Vec{20, -10, 0}}, {P: Vec{30, 10, 4}}, {P: Vec{40, 0, 4}}}},
		{Name: "bspline-closed", Type: "bspline", Closed: true, Points: []sharedPoint{
			{P: Vec{10, 0, 0}}, {P: Vec{10, 10, 0}}, {P: Vec{-10, 10, 0}}, {P: Vec{-10, 0, 0}}}},
	}
}

func fill(c *sharedCase) {
	p := c.path()
	cv, err := New(p)
	if err != nil {
		panic(err)
	}
	c.Length = cv.Length()
	c.Flat = len(cv.Resample(0.1))
	c.Segments = len(cv.Segs)
	c.Samples = nil
	for _, t := range []float64{0, 0.125, 0.25, 0.5, 0.75, 0.9, 1} {
		e := cv.At(t)
		c.Samples = append(c.Samples, sampleExp{T: t, P: e.P, D: e.T})
	}
}

func TestSharedCases(t *testing.T) {
	file := filepath.FromSlash(sharedFile)
	if *update {
		cases := sharedInputs()
		for i := range cases {
			fill(&cases[i])
		}
		b, _ := json.MarshalIndent(cases, "", " ")
		if err := os.WriteFile(file, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/geometry/curve -run SharedCases -update)", err)
	}
	var cases []sharedCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != len(sharedInputs()) {
		t.Fatalf("golden has %d cases, inputs %d: regenerate with -update", len(cases), len(sharedInputs()))
	}
	for _, c := range cases {
		cv, err := New(c.path())
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if math.Abs(cv.Length()-c.Length) > 1e-9*math.Max(1, c.Length) {
			t.Errorf("%s: length %v want %v", c.Name, cv.Length(), c.Length)
		}
		if got := len(cv.Resample(0.1)); got != c.Flat {
			t.Errorf("%s: flatten %d points want %d", c.Name, got, c.Flat)
		}
		for _, s := range c.Samples {
			e := cv.At(s.T)
			if !nearV(e.P, s.P, 1e-9) || !nearV(e.T, s.D, 1e-9) {
				t.Errorf("%s t=%v: %v/%v want %v/%v", c.Name, s.T, e.P, e.T, s.P, s.D)
			}
		}
	}
}
