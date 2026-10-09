package toolpath

import (
	"bytes"
	"math"
	"os"
	"testing"
)

func sample() *SliceResult {
	r := &SliceResult{
		Layers: []Layer{{Z: 0.2, First: 0, Count: 2}, {Z: 0.4, First: 2, Count: 1}},
		Segments: []Segment{
			{X0: 0, Y0: 0, Z0: 0.2, X1: 10, Y1: 0, Z1: 0.2, Kind: ExternalPerimeter, E: 0.5, F: 10},
			{X0: 10, Y0: 0, Z0: 0.2, X1: 10, Y1: 10, Z1: 0.2, Kind: Travel, F: 100},
			{X0: 10, Y0: 10, Z0: 0.4, X1: 0, Y1: 10, Z1: 0.4, Kind: Support, E: 0.4, F: 20},
		},
	}
	r.Finalize(FinalizeParams{FilamentDiameter: 1.75, FilamentDensity: 1.24, LayerHeight: 0.2, Nozzle: 0.4})
	return r
}

func TestFinalizeTimes(t *testing.T) {
	r := sample()
	// 10mm@10 = 1s, 10mm@100 = .1s, 10mm@20 = .5s
	if math.Abs(r.Duration-1.6) > 1e-5 {
		t.Fatalf("duration %v", r.Duration)
	}
	if r.Segments[1].T0 != 1 || math.Abs(float64(r.Segments[2].T0)-1.1) > 1e-6 {
		t.Fatalf("start times %v %v", r.Segments[1].T0, r.Segments[2].T0)
	}
	if r.Layers[1].T0 != r.Segments[2].T0 {
		t.Fatal("layer start time must equal its first segment's")
	}
	if !r.Supports {
		t.Fatal("support segment must set Supports")
	}
	if r.Segments[1].Width != 0 || r.Segments[0].Width <= 0 {
		t.Fatalf("widths: travel %v extrusion %v", r.Segments[1].Width, r.Segments[0].Width)
	}
	if want := 0.9; math.Abs(r.FilamentMM-want) > 1e-6 {
		t.Fatalf("filament %v", r.FilamentMM)
	}
	if r.FilamentG <= 0 {
		t.Fatal("mass must be positive")
	}
}

func TestSeekLookups(t *testing.T) {
	r := sample()
	for _, c := range []struct {
		t    float64
		want int
	}{{-1, 0}, {0, 0}, {0.99, 0}, {1.0, 1}, {1.09, 1}, {float64(r.Segments[2].T0), 2}, {99, 2}} {
		if got := r.SegmentAt(c.t); got != c.want {
			t.Errorf("SegmentAt(%v) = %d, want %d", c.t, got, c.want)
		}
	}
	for i, want := range []int{0, 0, 1} {
		if got := r.LayerAt(i); got != want {
			t.Errorf("LayerAt(%d) = %d, want %d", i, got, want)
		}
	}
	if (&SliceResult{}).SegmentAt(1) != -1 {
		t.Fatal("empty result has no segment")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	r := sample()
	r.Preview = true
	b, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Segments) != 3 || len(got.Layers) != 2 || !got.Preview || !got.Supports {
		t.Fatalf("round trip lost data: %+v", got)
	}
	for i := range r.Segments {
		a, b := r.Segments[i], got.Segments[i]
		if a.X1 != b.X1 || a.Kind != b.Kind || a.T0 != b.T0 || a.Width != b.Width || a.Dur != b.Dur {
			t.Fatalf("segment %d: %+v != %+v", i, a, b)
		}
	}
	if got.BBox != r.BBox || got.Duration != float64(float32(r.Duration)) {
		t.Fatalf("summary mismatch: %v %v", got.BBox, got.Duration)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	r := sample()
	b, _ := Encode(r)
	for name, bad := range map[string][]byte{
		"empty":     nil,
		"magic":     append([]byte("XXXX"), b[4:]...),
		"truncated": b[:len(b)-1],
		"kind":      func() []byte { c := append([]byte(nil), b...); c[headerSize+2*layerSize+36] = 99; return c }(),
	} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestValidateLayerIndex(t *testing.T) {
	r := sample()
	r.Layers[1].First = 5
	if r.Validate() == nil {
		t.Fatal("out-of-order layer index must be rejected")
	}
}

// The front end decodes this very file in frontend/printsim-model.test.mjs, so a change to the
// wire format fails here and there. Regenerate with UPDATE_GOLDEN=1 go test ./internal/printsim/toolpath.
func TestGoldenFixture(t *testing.T) {
	r := sample()
	r.Preview = true
	got, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	const path = "testdata/sample.scps"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("wire format changed: update the golden file and the front end decoder together")
	}
}
