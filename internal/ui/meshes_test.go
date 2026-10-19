package ui

import (
	"encoding/binary"
	"math"
	"net/http"
	"strings"
	"testing"
)

func cubeSTLBytes(s float32) []byte {
	p := [8][3]float32{{0, 0, 0}, {s, 0, 0}, {s, s, 0}, {0, s, 0}, {0, 0, s}, {s, 0, s}, {s, s, s}, {0, s, s}}
	q := [][4]int{{0, 3, 2, 1}, {4, 5, 6, 7}, {0, 1, 5, 4}, {1, 2, 6, 5}, {2, 3, 7, 6}, {3, 0, 4, 7}}
	var tris [][3]int
	for _, f := range q {
		tris = append(tris, [3]int{f[0], f[1], f[2]}, [3]int{f[0], f[2], f[3]})
	}
	b := make([]byte, 84+50*len(tris))
	binary.LittleEndian.PutUint32(b[80:], uint32(len(tris)))
	for i, t := range tris {
		for j, v := range t {
			for k := 0; k < 3; k++ {
				binary.LittleEndian.PutUint32(b[84+50*i+12+12*j+4*k:], math.Float32bits(p[v][k]))
			}
		}
	}
	return b
}

func TestMeshUploadEndpointValidatesBeforeImporting(t *testing.T) {
	srv, ts := testServer(t)
	good := cubeSTLBytes(10)

	if code, _ := post(t, ts.URL, "", "/api/mesh?name=cube.stl", good); code != http.StatusForbidden {
		t.Fatalf("an upload needs the token: %d", code)
	}
	if code, out := post(t, ts.URL, srv.Token, "/api/mesh?name=cube.txt", good); code != 400 || !strings.Contains(out["error"].(string), ".stl") {
		t.Fatalf("only .stl: %d %v", code, out)
	}
	// A mesh with a hole is refused with the reason, and nothing is added.
	holed := append([]byte{}, good[:len(good)-50]...)
	binary.LittleEndian.PutUint32(holed[80:], 11)
	code, out := post(t, ts.URL, srv.Token, "/api/mesh?name=holed.stl", holed)
	if code != 400 || !strings.Contains(out["error"].(string), "not watertight") || out["code"] != "mesh_has_holes" {
		t.Fatalf("holed mesh: %d %v", code, out)
	}
	srv.Svc.mu.Lock()
	n := len(srv.Svc.app.Graph.Nodes)
	srv.Svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("a refused mesh changed the model (%d nodes)", n)
	}

	// ...and sent again with the holes to be closed it is imported.
	if code, out := post(t, ts.URL, srv.Token, "/api/mesh?name=holed.stl&fill_holes=1", holed); code != 200 || out["ok"] != true {
		t.Fatalf("holed mesh with fill_holes: %d %v", code, out)
	}
	code, out = post(t, ts.URL, srv.Token, "/api/mesh?name=cube.stl&units=cm&placement=base", good)
	if code != 200 || out["ok"] != true {
		t.Fatalf("import: %d %v", code, out)
	}
	srv.Svc.mu.Lock()
	node := srv.Svc.app.Graph.Get("cube")
	srv.Svc.mu.Unlock()
	if node == nil || node.Type != "import_mesh" || node.Parameters["units"] != "cm" || node.Parameters["source"] != "cube.stl" {
		t.Fatalf("node: %+v", node)
	}
}
