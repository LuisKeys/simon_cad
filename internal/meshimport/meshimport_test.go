package meshimport

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// cube returns a closed, outward-oriented cube of side s with its corner at o.
func cube(o vec, s float64) [][3]vec {
	v := func(x, y, z float64) vec { return vec{o[0] + x*s, o[1] + y*s, o[2] + z*s} }
	p := [8]vec{v(0, 0, 0), v(1, 0, 0), v(1, 1, 0), v(0, 1, 0), v(0, 0, 1), v(1, 0, 1), v(1, 1, 1), v(0, 1, 1)}
	q := [][4]int{{0, 3, 2, 1}, {4, 5, 6, 7}, {0, 1, 5, 4}, {1, 2, 6, 5}, {2, 3, 7, 6}, {3, 0, 4, 7}}
	var out [][3]vec
	for _, f := range q {
		out = append(out, [3]vec{p[f[0]], p[f[1]], p[f[2]]}, [3]vec{p[f[0]], p[f[2]], p[f[3]]})
	}
	return out
}

func binarySTL(tris [][3]vec) []byte {
	b := make([]byte, 84+50*len(tris))
	copy(b, "test")
	binary.LittleEndian.PutUint32(b[80:], uint32(len(tris)))
	for i, t := range tris {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				binary.LittleEndian.PutUint32(b[84+50*i+12+12*j+4*k:], math.Float32bits(float32(t[j][k])))
			}
		}
	}
	return b
}

func asciiSTL(tris [][3]vec) []byte {
	var sb strings.Builder
	sb.WriteString("solid part\n")
	for _, t := range tris {
		sb.WriteString("  facet normal 0 0 0\n    outer loop\n")
		for _, p := range t {
			fmt.Fprintf(&sb, "      vertex %g %g %g\n", p[0], p[1], p[2])
		}
		sb.WriteString("    endloop\n  endfacet\n")
	}
	sb.WriteString("endsolid part\n")
	return []byte(sb.String())
}

func sphere(r float64, rings, segs int) [][3]vec {
	pt := func(i, j int) vec {
		th, ph := math.Pi*float64(i)/float64(rings), 2*math.Pi*float64(j%segs)/float64(segs)
		if i == 0 || i == rings {
			return vec{0, 0, r * math.Cos(th) / math.Abs(math.Cos(th))}
		}
		return vec{r * math.Sin(th) * math.Cos(ph), r * math.Sin(th) * math.Sin(ph), r * math.Cos(th)}
	}
	var out [][3]vec
	for i := 0; i < rings; i++ {
		for j := 0; j < segs; j++ {
			a, b, c, d := pt(i, j), pt(i+1, j), pt(i+1, j+1), pt(i, j+1)
			if i == 0 {
				out = append(out, [3]vec{a, b, c})
			} else if i == rings-1 {
				out = append(out, [3]vec{a, b, d})
			} else {
				out = append(out, [3]vec{a, b, c}, [3]vec{a, c, d})
			}
		}
	}
	return out
}

func TestAcceptsCubeAsBinaryAndASCIIWithTheSameID(t *testing.T) {
	tris := cube(vec{}, 10)
	b, err := Prepare(binarySTL(tris), Options{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Prepare(asciiSTL(tris), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || !ValidID(a.ID) {
		t.Fatalf("ids differ or are malformed: %q vs %q", a.ID, b.ID)
	}
	if a.Report.Format != "ascii" || b.Report.Format != "binary" {
		t.Fatalf("formats: %s %s", a.Report.Format, b.Report.Format)
	}
	if math.Abs(b.Report.Volume-1000) > 1e-6 || b.Report.Triangles != 12 || b.Report.Vertices != 8 {
		t.Fatalf("report: %+v", b.Report)
	}
	if m, err := Decode(b.ID, b.Data); err != nil || m.Triangles() != 12 {
		t.Fatalf("decode: %v", err)
	}
	if _, err := Decode(b.ID, append([]byte{}, a.Data[:len(a.Data)-1]...)); err == nil {
		t.Fatal("a corrupted stored mesh must be refused")
	}
}

func TestAcceptsSphere(t *testing.T) {
	p, err := Prepare(binarySTL(sphere(5, 16, 24)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := 4.0 / 3 * math.Pi * 125
	if p.Report.Volume < 0.9*want || p.Report.Volume > want {
		t.Fatalf("volume %g, sphere is %g", p.Report.Volume, want)
	}
}

func TestInsideOutMeshIsFlipped(t *testing.T) {
	tris := cube(vec{}, 4)
	for i := range tris {
		tris[i][1], tris[i][2] = tris[i][2], tris[i][1]
	}
	p, err := Prepare(binarySTL(tris), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Report.Flipped || p.Report.Volume < 63.99 || p.Report.Volume > 64.01 || len(p.Report.Warnings) == 0 {
		t.Fatalf("report: %+v", p.Report)
	}
}

func TestRejects(t *testing.T) {
	sph := sphere(5, 12, 16)
	flip := cube(vec{}, 3)
	flip[3][1], flip[3][2] = flip[3][2], flip[3][1]
	dup := append(cube(vec{}, 3), cube(vec{}, 3)[0])
	twisted := cube(vec{}, 4)
	for i := range twisted {
		for j := range twisted[i] {
			if twisted[i][j] == (vec{4, 4, 4}) {
				twisted[i][j] = vec{4, 4, -2}
			}
		}
	}
	touching := append(cube(vec{}, 2), cube(vec{2, 2, 2}, 2)...) // two cubes meeting at one corner
	nan := binarySTL(cube(vec{}, 2))
	binary.LittleEndian.PutUint32(nan[84+12:], math.Float32bits(float32(math.NaN())))
	tooFew := binarySTL(cube(vec{}, 2)[:3])
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"empty", nil, "empty"},
		{"garbage", []byte("this is not a mesh at all, really not"), "not a mesh file"},
		{"truncated binary", binarySTL(cube(vec{}, 2))[:200], "truncated"},
		{"trailing bytes", append(binarySTL(cube(vec{}, 2)), 1, 2, 3), "trailing"},
		{"nan", nan, "non-finite"},
		{"ascii truncated", asciiSTL(cube(vec{}, 2))[:300], ""},
		{"ascii two solids", append(asciiSTL(cube(vec{}, 2)), asciiSTL(cube(vec{5, 5, 5}, 2))...), "several solids"},
		{"hole", binarySTL(sph[1:]), "not watertight"},
		{"flipped neighbour", binarySTL(flip), "orientation"},
		{"duplicate triangle", binarySTL(dup), "non-manifold geometry"},
		{"too few", tooFew, "at least 4"},
		{"two shells", binarySTL(append(cube(vec{}, 2), cube(vec{10, 0, 0}, 2)...)), "separate shells"},
		{"touching corner", binarySTL(touching), "non-manifold vertex"},
		{"self-intersecting", binarySTL(twisted), "intersects itself"},
		{"flat", binarySTL(cube(vec{}, 0.0000001)), "is a point"},
	}
	for _, c := range cases {
		_, err := Prepare(c.data, Options{})
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if c.want != "" && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q does not mention %q", c.name, err, c.want)
		}
	}
}

func TestTriangleLimit(t *testing.T) {
	data := binarySTL(sphere(5, 16, 24))
	if _, err := Prepare(data, Options{MaxTriangles: 100}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit not enforced: %v", err)
	}
	if _, err := Prepare(asciiSTL(sphere(5, 16, 24)), Options{MaxTriangles: 100}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("ascii limit not enforced: %v", err)
	}
}

func TestIDsAreSafeFileNames(t *testing.T) {
	for _, id := range []string{"", "mesh_", "mesh_../../etc/passwd", "mesh_ABCDEF012345678901234567", "mesh_zz34567890123456789012zz", "img_0123456789abcdef01234567"} {
		if ValidID(id) {
			t.Errorf("%q accepted", id)
		}
	}
	if !ValidID("mesh_0123456789abcdef01234567") {
		t.Error("well-formed id rejected")
	}
}

func TestBigMeshIsFast(t *testing.T) {
	if _, err := Prepare(binarySTL(sphere(50, 100, 130)), Options{}); err != nil {
		t.Fatal(err)
	}
}

func sharesVertex(a, b [3]vec) int {
	n := 0
	for _, p := range a {
		for _, q := range b {
			if p == q {
				n++
			}
		}
	}
	return n
}

func TestFillHolesClosesWhatWasOpenAndOnlyThat(t *testing.T) {
	box := cube(vec{}, 10)
	open := box[2:] // the bottom face (two triangles) is missing
	if _, err := Prepare(binarySTL(open), Options{}); err == nil {
		t.Fatal("holes are refused unless closing them is asked for")
	} else if e, ok := err.(*Error); !ok || !e.HolesOnly {
		t.Fatalf("a mesh whose only fault is a hole should say so: %v", err)
	}
	p, err := Prepare(binarySTL(open), Options{FillHoles: true})
	if err != nil {
		t.Fatal(err)
	}
	if f := p.Report.Filled; f == nil || f.Holes != 1 || f.Triangles != 2 || f.Largest != 4 {
		t.Fatalf("fill report: %+v", p.Report.Filled)
	}
	if math.Abs(p.Report.Volume-1000) > 1e-6 || p.Report.Triangles != 12 || len(p.Report.Warnings) == 0 {
		t.Fatalf("report: %+v", p.Report)
	}
	// Nothing to close: the option changes nothing and reports nothing.
	q, err := Prepare(binarySTL(box), Options{FillHoles: true})
	if err != nil || q.Report.Filled != nil {
		t.Fatalf("closed mesh: %v %+v", err, q.Report.Filled)
	}
}

func TestFillHolesOnACurvedSurface(t *testing.T) {
	sph := sphere(5, 16, 24)
	want, err := Prepare(binarySTL(sph), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Remove a patch of neighbouring triangles around the equator.
	var kept [][3]vec
	removed := 0
	for i, tr := range sph {
		if i >= 180 && i < 186 {
			removed++
			continue
		}
		kept = append(kept, tr)
	}
	p, err := Prepare(binarySTL(kept), Options{FillHoles: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Report.Filled == nil || p.Report.Filled.Holes < 1 {
		t.Fatalf("expected a hole to be closed: %+v", p.Report)
	}
	if math.Abs(p.Report.Volume-want.Report.Volume)/want.Report.Volume > 0.02 {
		t.Fatalf("volume %g after closing, %g originally", p.Report.Volume, want.Report.Volume)
	}
}

func TestFillHolesWhereTwoHolesTouchAtAVertex(t *testing.T) {
	sph := sphere(5, 16, 24)
	i, j := -1, -1
find:
	for a := range sph {
		for b := a + 1; b < len(sph); b++ {
			if sharesVertex(sph[a], sph[b]) == 1 && a > 40 && b < 300 {
				i, j = a, b
				break find
			}
		}
	}
	if i < 0 {
		t.Fatal("no pair of triangles meeting at one vertex")
	}
	var kept [][3]vec
	for k, tr := range sph {
		if k != i && k != j {
			kept = append(kept, tr)
		}
	}
	p, err := Prepare(binarySTL(kept), Options{FillHoles: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Report.Filled == nil || p.Report.Filled.Holes != 2 || p.Report.Filled.Triangles != 2 {
		t.Fatalf("two single-triangle holes expected: %+v", p.Report.Filled)
	}
}

func TestFillHolesRefusesWhatItCannotInvent(t *testing.T) {
	// A box missing three of its six faces: that is not a gap, it is a missing part.
	if _, err := Prepare(binarySTL(cube(vec{}, 10)[6:]), Options{FillHoles: true}); err == nil || !strings.Contains(err.Error(), "missing a whole part") {
		t.Fatalf("a missing half should be refused: %v", err)
	}
	// Other faults are not holes, and closing holes does not hide them.
	dup := append(cube(vec{}, 3), cube(vec{}, 3)[0])
	_, err := Prepare(binarySTL(dup), Options{FillHoles: true})
	if e, ok := err.(*Error); !ok || e.HolesOnly {
		t.Fatalf("a duplicated triangle is not a hole: %v", err)
	}
	// The triangle limit still holds after the patches.
	if _, err := Prepare(binarySTL(cube(vec{}, 3)[2:]), Options{FillHoles: true, MaxTriangles: 11}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit after patching: %v", err)
	}
}

// gridCube is a cube whose faces are n x n quads, so most of its triangles are redundant.
func gridCube(s float64, n int) [][3]vec {
	var out [][3]vec
	face := func(o, du, dv vec) {
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				p := func(a, b int) vec {
					f, g := float64(a)/float64(n), float64(b)/float64(n)
					return vec{o[0] + du[0]*f + dv[0]*g, o[1] + du[1]*f + dv[1]*g, o[2] + du[2]*f + dv[2]*g}
				}
				a, b, c, d := p(i, j), p(i+1, j), p(i+1, j+1), p(i, j+1)
				out = append(out, [3]vec{a, b, c}, [3]vec{a, c, d})
			}
		}
	}
	x, y, z := vec{s, 0, 0}, vec{0, s, 0}, vec{0, 0, s}
	face(vec{}, y, x)        // bottom, normal -z
	face(z, x, y)            // top, +z
	face(vec{}, x, z)        // front, -y
	face(vec{0, s, 0}, z, x) // back, +y
	face(vec{}, z, y)        // left, -x
	face(vec{s, 0, 0}, y, z) // right, +x
	return out
}

func TestDecimateKeepsAFlatSolidExact(t *testing.T) {
	data := binarySTL(gridCube(10, 12)) // 1728 triangles
	src, err := Prepare(data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	red, err := Reduce(src.Mesh, 100)
	if err != nil {
		t.Fatal(err)
	}
	r := red.Report.Reduced
	if r == nil || r.From != 1728 || red.Report.Triangles > 120 {
		t.Fatalf("not reduced: %+v", red.Report)
	}
	if math.Abs(red.Report.Volume-1000) > 1e-3 || math.Abs(r.VolumeChange) > 1e-6 {
		t.Fatalf("a cube must stay a cube: volume %g (%g)", red.Report.Volume, r.VolumeChange)
	}
	for k := 0; k < 3; k++ {
		if math.Abs(red.Report.Min[k]) > 1e-4 || math.Abs(red.Report.Max[k]-10) > 1e-4 {
			t.Fatalf("the corners must stay put: %v %v", red.Report.Min, red.Report.Max)
		}
	}
	if src.ID == red.ID {
		t.Fatal("a reduced mesh is a different mesh")
	}
	if _, err := Decode(red.ID, red.Data); err != nil {
		t.Fatal(err)
	}
}

func TestDecimateACurvedSurface(t *testing.T) {
	src, err := Prepare(binarySTL(sphere(25, 100, 140)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	red, err := Reduce(src.Mesh, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if n := red.Report.Triangles; n < 2900 || n > 3300 {
		t.Fatalf("asked for 3000, got %d", n)
	}
	if v := red.Report.Reduced.VolumeChange; v > 0 || v < -0.05 {
		t.Fatalf("a reduced sphere shrinks a little, got %g", v)
	}
}

func TestPrepareCanDecimateWhileImporting(t *testing.T) {
	p, err := Prepare(binarySTL(gridCube(10, 12)), Options{DecimateTo: 200})
	if err != nil {
		t.Fatal(err)
	}
	if p.Report.Reduced == nil || p.Report.Triangles > 220 || p.Mesh.Triangles() != p.Report.Triangles {
		t.Fatalf("report: %+v", p.Report)
	}
	// Asking for more than the mesh has changes nothing.
	q, err := Prepare(binarySTL(cube(vec{}, 4)), Options{DecimateTo: 500})
	if err != nil || q.Report.Reduced != nil {
		t.Fatalf("%v %+v", err, q.Report.Reduced)
	}
}

func TestDecimateRefusesWhatItCannotDo(t *testing.T) {
	src, _ := Prepare(binarySTL(cube(vec{}, 4)), Options{})
	if _, err := Reduce(src.Mesh, 500); err == nil {
		t.Fatal("cannot reduce to more triangles than it has")
	}
	if _, err := Decimate(src.Mesh, 4); err == nil || !strings.Contains(err.Error(), "below") {
		t.Fatalf("below the minimum: %v", err)
	}
}

func TestDecimateBigMeshIsFast(t *testing.T) {
	src, err := Prepare(binarySTL(sphere(25, 190, 190)), Options{}) // 71k triangles
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	red, err := Reduce(src.Mesh, 10000)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d -> %d triangles in %v (volume %+.2f%%)", src.Report.Triangles, red.Report.Triangles, time.Since(start), 100*red.Report.Reduced.VolumeChange)
	if time.Since(start) > 20*time.Second {
		t.Fatal("too slow")
	}
}
