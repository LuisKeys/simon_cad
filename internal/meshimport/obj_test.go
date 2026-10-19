package meshimport

import (
	"fmt"
	"strings"
	"testing"
)

// cubeOBJ writes a cube of side s as quads, with texture and normal indices and a comment.
func cubeOBJ(s int, face string) string {
	var sb strings.Builder
	sb.WriteString("# cube\nmtllib x.mtl\no cube\n")
	for _, c := range [][3]int{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}, {0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1}} {
		fmt.Fprintf(&sb, "v %d %d %d\n", c[0]*s, c[1]*s, c[2]*s)
	}
	sb.WriteString("vt 0 0\nvn 0 0 1\nusemtl m\ns off\n")
	for _, f := range [][4]int{{1, 4, 3, 2}, {5, 6, 7, 8}, {1, 2, 6, 5}, {2, 3, 7, 6}, {3, 4, 8, 7}, {4, 1, 5, 8}} {
		fmt.Fprintf(&sb, face+"\n", f[0], f[1], f[2], f[3])
	}
	return sb.String()
}

func TestOBJQuadsAreTriangulated(t *testing.T) {
	for name, face := range map[string]string{"plain": "f %d %d %d %d", "slashes": "f %d/1/1 %d/1/1 %d/1/1 %d/1/1", "no uv": "f %d//1 %d//1 %d//1 %d//1"} {
		p, err := Prepare([]byte(cubeOBJ(10, face)), Options{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Report.Format != "obj" || p.Report.Triangles != 12 || p.Report.Volume < 999.99 || p.Report.Volume > 1000.01 {
			t.Fatalf("%s: %+v", name, p.Report)
		}
	}
}

func TestOBJSameIDAsSTL(t *testing.T) {
	o, err := Prepare([]byte(cubeOBJ(4, "f %d %d %d %d")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Triangulation differs from cube(), so compare volumes and a re-import of the canonical form.
	if o.Report.Volume < 63.99 || o.Report.Volume > 64.01 {
		t.Fatalf("volume %g", o.Report.Volume)
	}
	again, err := Prepare(o.Data, Options{})
	if err != nil || again.ID != o.ID {
		t.Fatalf("canonical STL of an OBJ must keep its id: %v", err)
	}
}

func TestOBJNegativeIndicesAndCRLF(t *testing.T) {
	src := "v 0 0 0\r\nv 1 0 0\r\nv 0 1 0\r\nv 0 0 1\r\nf -4 -2 -3\r\nf -4 -3 -1\r\nf -4 -1 -2\r\nf -3 -2 -1\r\n"
	p, err := Prepare([]byte(src), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Report.Triangles != 4 {
		t.Fatalf("%+v", p.Report)
	}
}

func TestOBJConcaveFace(t *testing.T) {
	// An L-shaped prism: the two L faces are concave hexagons.
	l := [][2]int{{0, 0}, {2, 0}, {2, 1}, {1, 1}, {1, 2}, {0, 2}}
	var sb strings.Builder
	for z := 0; z < 2; z++ {
		for _, c := range l {
			fmt.Fprintf(&sb, "v %d %d %d\n", c[0], c[1], z)
		}
	}
	sb.WriteString("f 6 5 4 3 2 1\nf 7 8 9 10 11 12\n")
	for i := 0; i < 6; i++ {
		j := (i + 1) % 6
		fmt.Fprintf(&sb, "f %d %d %d %d\n", i+1, j+1, j+7, i+7)
	}
	p, err := Prepare([]byte(sb.String()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Report.Volume < 2.99 || p.Report.Volume > 3.01 {
		t.Fatalf("volume %g, want 3", p.Report.Volume)
	}
}

func TestOBJRejects(t *testing.T) {
	cube := cubeOBJ(2, "f %d %d %d %d")
	cases := map[string]struct{ src, want string }{
		"no faces":        {"v 0 0 0\nv 1 0 0\nv 0 1 0\n", "no faces"},
		"bad index":       {strings.Replace(cube, "f 1 4 3 2", "f 1 4 3 99", 1), "vertex 99"},
		"zero index":      {strings.Replace(cube, "f 1 4 3 2", "f 0 4 3 2", 1), "vertex index"},
		"short face":      {strings.Replace(cube, "f 1 4 3 2", "f 1 4", 1), "at least 3"},
		"bad number":      {strings.Replace(cube, "v 2 0 0", "v two 0 0", 1), "number"},
		"huge coordinate": {strings.Replace(cube, "v 2 0 0", "v 1e30 0 0", 1), "absurd"},
		"free-form":       {cube + "curv 0 1 1 2\n", "free-form"},
		"unknown":         {cube + "banana 1\n", "unknown statement"},
		"hole":            {strings.Replace(cube, "f 1 4 3 2\n", "", 1), "not watertight"},
	}
	for name, c := range cases {
		_, err := Prepare([]byte(c.src), Options{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestOBJTriangleLimit(t *testing.T) {
	if _, err := Prepare([]byte(cubeOBJ(2, "f %d %d %d %d")), Options{MaxTriangles: 8}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("got %v", err)
	}
}
