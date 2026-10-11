package rendering_test

import (
	"math"
	"testing"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/rendering"
)

func TestPathsAreRenderedWithControlPointsAndUsers(t *testing.T) {
	a := app.New("t", clock)
	defer a.Close()
	run(t, a, "create_path", `{"id":"spine","type":"bezier","closed":false,"points":[
		{"p":[0,0,0],"out":[10,0,0],"w":2},{"p":[30,10,0],"in":[20,10,0],"twist":45}]}`)
	run(t, a, "create_profile", `{"id":"pr","shape":"circle","radius":1}`)
	m, _ := a.Model()
	d, err := rendering.New().Scene(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Paths) != 0 { // Scene alone does not know the graph: the service fills Paths
		t.Fatalf("scene should leave paths to the caller, got %d", len(d.Paths))
	}
	paths := rendering.PathsOf(a.Snapshot(), m)
	if len(paths) != 1 {
		t.Fatalf("paths %d", len(paths))
	}
	p := paths[0]
	if p.ID != "spine" || p.Type != "bezier" || p.Closed || len(p.Points) != 2 {
		t.Fatalf("%+v", p)
	}
	if p.Points[0].Out == nil || p.Points[0].W != 2 || p.Points[1].In == nil || p.Points[1].Twist != 45 {
		t.Fatalf("control data lost: %+v", p.Points)
	}
	if len(p.Lines) < 12 || len(p.Lines)%6 != 0 {
		t.Fatalf("polyline floats %d", len(p.Lines))
	}
	if p.Length < 31 || p.Length > 40 {
		t.Fatalf("length %v", p.Length)
	}
	// the polyline ends at the end point
	n := len(p.Lines)
	if math.Abs(float64(p.Lines[n-3])-30) > 1e-3 || math.Abs(float64(p.Lines[n-2])-10) > 1e-3 {
		t.Fatalf("last point %v", p.Lines[n-3:])
	}
	if len(p.Users) != 0 {
		t.Fatalf("users %v", p.Users)
	}
	run(t, a, "create_sweep", `{"id":"rod","path":"spine","profile":"pr"}`)
	m, _ = a.Model()
	if u := rendering.PathsOf(a.Snapshot(), m)[0].Users; len(u) != 1 || u[0] != "rod" {
		t.Fatalf("users %v", u)
	}
	// a failing path (bad handles) is not drawn
	run(t, a, "create_path", `{"id":"ok2","points":[[0,0,0],[1,0,0]]}`)
	m, _ = a.Model()
	if n := len(rendering.PathsOf(a.Snapshot(), m)); n != 2 {
		t.Fatalf("paths %d", n)
	}
}
