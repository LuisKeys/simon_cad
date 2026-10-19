package rebuild

import (
	"encoding/binary"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/meshimport"
)

type memMeshes map[string]*meshimport.Mesh

func (m memMeshes) Mesh(id string) (*meshimport.Mesh, error) {
	if x, ok := m[id]; ok {
		return x, nil
	}
	return nil, fmt.Errorf("mesh %s is not in the project", id)
}

func stl(tris [][9]float32) []byte {
	b := make([]byte, 84+50*len(tris))
	binary.LittleEndian.PutUint32(b[80:], uint32(len(tris)))
	for i, t := range tris {
		for j, v := range t {
			binary.LittleEndian.PutUint32(b[84+50*i+12+4*j:], math.Float32bits(v))
		}
	}
	return b
}

// boxSTL is a closed box [x0,x0+dx] x [y0,y0+dy] x [z0,z0+dz].
func boxSTL(x0, y0, z0, dx, dy, dz float32) []byte {
	p := [8][3]float32{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}, {0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1}}
	q := [][4]int{{0, 3, 2, 1}, {4, 5, 6, 7}, {0, 1, 5, 4}, {1, 2, 6, 5}, {2, 3, 7, 6}, {3, 0, 4, 7}}
	var tris [][9]float32
	v := func(i int) [3]float32 { return [3]float32{x0 + p[i][0]*dx, y0 + p[i][1]*dy, z0 + p[i][2]*dz} }
	for _, f := range q {
		a, b, c, d := v(f[0]), v(f[1]), v(f[2]), v(f[3])
		tris = append(tris, [9]float32{a[0], a[1], a[2], b[0], b[1], b[2], c[0], c[1], c[2]},
			[9]float32{a[0], a[1], a[2], c[0], c[1], c[2], d[0], d[1], d[2]})
	}
	return stl(tris)
}

func sphereSTL(r float64, rings, segs int) []byte {
	pt := func(i, j int) [3]float32 {
		th, ph := math.Pi*float64(i)/float64(rings), 2*math.Pi*float64(j%segs)/float64(segs)
		if i == 0 {
			return [3]float32{0, 0, float32(r)}
		}
		if i == rings {
			return [3]float32{0, 0, -float32(r)}
		}
		return [3]float32{float32(r * math.Sin(th) * math.Cos(ph)), float32(r * math.Sin(th) * math.Sin(ph)), float32(r * math.Cos(th))}
	}
	var tris [][9]float32
	add := func(a, b, c [3]float32) {
		tris = append(tris, [9]float32{a[0], a[1], a[2], b[0], b[1], b[2], c[0], c[1], c[2]})
	}
	for i := 0; i < rings; i++ {
		for j := 0; j < segs; j++ {
			a, b, c, d := pt(i, j), pt(i+1, j), pt(i+1, j+1), pt(i, j+1)
			switch i {
			case 0:
				add(a, b, c)
			case rings - 1:
				add(a, b, d)
			default:
				add(a, b, c)
				add(a, c, d)
			}
		}
	}
	return stl(tris)
}

func meshEngine(t *testing.T, data []byte) (*Engine, string) {
	p, err := meshimport.Prepare(data, meshimport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, e := setup(t)
	e.Meshes = memMeshes{p.ID: p.Mesh}
	return e, p.ID
}

func meshNode(id string, extra map[string]any) *cadir.Node {
	params := map[string]any{"mesh": id}
	for k, v := range extra {
		params[k] = v
	}
	return &cadir.Node{ID: "scan", Type: "import_mesh", Parameters: params}
}

func rebuildOne(t *testing.T, e *Engine, n *cadir.Node) *Model {
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{n}
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestImportMeshBecomesASolid(t *testing.T) {
	e, id := meshEngine(t, boxSTL(10, 20, 30, 40, 20, 5))
	m := rebuildOne(t, e, meshNode(id, nil))
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed: %v", m.Nodes[f[0]].Errors)
	}
	if got := m.Volume(); !near(got, 4000) {
		t.Fatalf("volume %v", got)
	}
	b := m.Nodes["scan"].Body
	if bd := b.Bounds(); !near(bd.Min[0], 10) || !near(bd.Min[1], 20) || !near(bd.Max[2], 35) {
		t.Fatalf("bounds %v", bd)
	}
}

func TestImportMeshUnitsAndPlacement(t *testing.T) {
	e, id := meshEngine(t, boxSTL(10, 20, 30, 40, 20, 5))
	m := rebuildOne(t, e, meshNode(id, map[string]any{"units": "cm", "placement": "base"}))
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed: %v", m.Nodes[f[0]].Errors)
	}
	if got := m.Volume(); !near(got, 4000*1000) {
		t.Fatalf("volume %v", got)
	}
	bd := m.Nodes["scan"].Body.Bounds()
	if !near(bd.Min[0], -200) || !near(bd.Min[1], -100) || !near(bd.Min[2], 0) || !near(bd.Max[0], 200) || !near(bd.Max[2], 50) {
		t.Fatalf("bounds %v", bd)
	}
}

func TestImportMeshErrors(t *testing.T) {
	e, id := meshEngine(t, boxSTL(0, 0, 0, 1, 1, 1))
	for name, n := range map[string]*cadir.Node{
		"no mesh":       meshNode("", nil),
		"unknown mesh":  meshNode("mesh_0123456789abcdef01234567", nil),
		"bad units":     meshNode(id, map[string]any{"units": "furlong"}),
		"bad placement": meshNode(id, map[string]any{"placement": "left"}),
	} {
		m := rebuildOne(t, e, n)
		if len(m.Failed()) != 1 {
			t.Errorf("%s: expected the node to fail", name)
		}
	}
}

func TestImportMeshCachesByContent(t *testing.T) {
	e, id := meshEngine(t, boxSTL(0, 0, 0, 3, 3, 3))
	rebuildOne(t, e, meshNode(id, nil))
	before := e.Stats
	rebuildOne(t, e, meshNode(id, nil))
	if e.Stats.Hits != before.Hits+1 || e.Stats.Misses != before.Misses {
		t.Fatalf("second rebuild was not a cache hit: %+v -> %+v", before, e.Stats)
	}
}

func TestImportedMeshTakesPartInBooleans(t *testing.T) {
	e, id := meshEngine(t, boxSTL(0, 0, 0, 10, 10, 10))
	g := cadir.NewGraph()
	g.Nodes = []*cadir.Node{meshNode(id, nil), plate("cutter", 5, 5, 20), {
		ID: "cut", Type: "subtract", Dependencies: []string{"scan", "cutter"},
		Parameters: map[string]any{"target": "scan", "tool": "cutter"}}}
	m, err := e.Rebuild(g)
	if err != nil {
		t.Fatal(err)
	}
	if f := m.Failed(); len(f) != 0 {
		t.Fatalf("failed: %v", m.Nodes[f[0]].Errors)
	}
	if got := m.Volume(); !near(got, 1000-5*5*10) {
		t.Fatalf("volume %v", got)
	}
}

func TestImportMeshSphereAndTiming(t *testing.T) {
	for _, c := range []struct{ rings, segs int }{{16, 24}, {60, 80}, {100, 140}} {
		data := sphereSTL(25, c.rings, c.segs)
		p, err := meshimport.Prepare(data, meshimport.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Run(fmt.Sprint(p.Report.Triangles), func(t *testing.T) {
			_, e := setup(t)
			e.Meshes = memMeshes{p.ID: p.Mesh}
			start := time.Now()
			m := rebuildOne(t, e, meshNode(p.ID, nil))
			if f := m.Failed(); len(f) != 0 {
				t.Fatalf("%v", m.Nodes[f[0]].Errors)
			}
			t.Logf("%d triangles: %v, volume %.1f of %.1f", p.Report.Triangles, time.Since(start), m.Volume(), p.Report.Volume)
		})
	}
}
