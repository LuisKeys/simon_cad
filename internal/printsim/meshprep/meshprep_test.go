package meshprep

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/printsim/profile"
)

type tri [3][3]float32

// writeSTL writes a binary STL.
func writeSTL(path string, tris []tri) error {
	b := make([]byte, 84+50*len(tris))
	binary.LittleEndian.PutUint32(b[80:], uint32(len(tris)))
	for i, t := range tris {
		o := 84 + 50*i + 12
		for _, v := range t {
			for _, c := range v {
				binary.LittleEndian.PutUint32(b[o:], math.Float32bits(c))
				o += 4
			}
		}
	}
	return os.WriteFile(path, b, 0o600)
}

// box returns the 12 outward-facing (CCW) triangles of an axis-aligned box.
func box(x0, y0, z0, x1, y1, z1 float32) []tri {
	p := func(x, y, z float32) [3]float32 { return [3]float32{x, y, z} }
	v := [8][3]float32{p(x0, y0, z0), p(x1, y0, z0), p(x1, y1, z0), p(x0, y1, z0), p(x0, y0, z1), p(x1, y0, z1), p(x1, y1, z1), p(x0, y1, z1)}
	q := func(a, b, c, d int) []tri { return []tri{{v[a], v[b], v[c]}, {v[a], v[c], v[d]}} }
	var out []tri
	for _, f := range [][4]int{{0, 3, 2, 1}, {4, 5, 6, 7}, {0, 1, 5, 4}, {1, 2, 6, 5}, {2, 3, 7, 6}, {3, 0, 4, 7}} {
		out = append(out, q(f[0], f[1], f[2], f[3])...)
	}
	return out
}

type fake struct {
	tris []tri
	err  error
	n    int
}

func (f *fake) ExportSTL(path string, _ float64) error {
	f.n++
	if f.err != nil {
		return f.err
	}
	return writeSTL(path, f.tris)
}

func TestPrepareCentresOnBedAndKeepsSource(t *testing.T) {
	dir := t.TempDir()
	// a 20×10×5 box floating at (100,-50,-3) in CAD space
	src := &fake{tris: box(90, -55, -3, 110, -45, 2)}
	p := profile.GenericFDM()
	got, err := Prepare(context.Background(), src, p, dir)
	if err != nil {
		t.Fatal(err)
	}
	// bed centre (110,110); model centre (100,-50); lowest point z=-3
	want := Placement{DX: 10, DY: 160, DZ: 3}
	if math.Abs(got.Placement.DX-want.DX) > 1e-4 || math.Abs(got.Placement.DY-want.DY) > 1e-4 || math.Abs(got.Placement.DZ-want.DZ) > 1e-4 {
		t.Fatalf("placement %+v, want %+v", got.Placement, want)
	}
	if math.Abs(got.Min[2]) > 1e-5 || math.Abs(got.Max[2]-5) > 1e-5 {
		t.Fatalf("model must sit on the plate: z [%v, %v]", got.Min[2], got.Max[2])
	}
	if cx := (got.Min[0] + got.Max[0]) / 2; math.Abs(cx-p.BuildX/2) > 1e-4 {
		t.Fatalf("not centred in X: %v", cx)
	}
	if cy := (got.Min[1] + got.Max[1]) / 2; math.Abs(cy-p.BuildY/2) > 1e-4 {
		t.Fatalf("not centred in Y: %v", cy)
	}
	// the written STL carries the translation, the raw export is gone
	tris, err := ReadTriangles(got.STLPath)
	if err != nil || len(tris) != 12*9 {
		t.Fatalf("read back: %v (%d floats)", err, len(tris))
	}
	if _, err := os.Stat(dir + "/model_raw.stl"); !os.IsNotExist(err) {
		t.Fatal("the raw export must be removed")
	}
	// the exporter's mesh (standing in for the CAD shape) is untouched: only the copy moved
	if src.tris[0][0][0] != 90 && src.tris[0][0][0] != 110 {
		t.Fatalf("source geometry was modified: %v", src.tris[0][0])
	}
	if first, _ := ReadTriangles(got.STLPath); first[0] == src.tris[0][0][0] {
		t.Fatal("the placed copy must differ from the source")
	}
}

func TestPrepareErrors(t *testing.T) {
	p := profile.GenericFDM()
	cases := map[string]struct {
		src  *fake
		want string
	}{
		"empty":    {&fake{tris: nil}, "no geometry"},
		"open":     {&fake{tris: box(0, 0, 0, 10, 10, 10)[1:]}, "watertight"},
		"too big":  {&fake{tris: box(0, 0, 0, 300, 10, 10)}, "does not fit"},
		"too tall": {&fake{tris: box(0, 0, 0, 10, 10, 400)}, "does not fit"},
		"exporter": {&fake{err: errors.New("kernel exploded")}, "kernel exploded"},
		"inverted": {&fake{tris: flip(box(0, 0, 0, 10, 10, 10))}, "inside-out"},
	}
	for name, c := range cases {
		_, err := Prepare(context.Background(), c.src, p, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func flip(ts []tri) []tri {
	out := make([]tri, len(ts))
	for i, t := range ts {
		out[i] = tri{t[0], t[2], t[1]}
	}
	return out
}

func TestPrepareHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := &fake{tris: box(0, 0, 0, 10, 10, 10)}
	if _, err := Prepare(ctx, src, profile.GenericFDM(), t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if src.n != 0 {
		t.Fatal("a cancelled prepare must not start the export")
	}
}
