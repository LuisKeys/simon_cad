package ui

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
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

func TestStepUploadEndpointChecksBeforeImporting(t *testing.T) {
	srv, ts := testServer(t)
	if code, _ := post(t, ts.URL, "", "/api/step?name=a.step", []byte("x")); code != http.StatusForbidden {
		t.Fatalf("an upload needs the token: %d", code)
	}
	if code, out := post(t, ts.URL, srv.Token, "/api/step?name=a.stl", []byte("x")); code != 400 || !strings.Contains(out["error"].(string), ".step") {
		t.Fatalf("only .step/.stp: %d %v", code, out)
	}
	if code, out := post(t, ts.URL, srv.Token, "/api/step?name=a.step", []byte("not a step file")); code != 400 || !strings.Contains(out["error"].(string), "not a STEP file") {
		t.Fatalf("garbage: %d %v", code, out)
	}
	srv.Svc.mu.Lock()
	n := len(srv.Svc.app.Graph.Nodes)
	srv.Svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("a refused file changed the model (%d nodes)", n)
	}

	// A STEP exported by the same kernel imports as an import_step body.
	srv.Svc.mu.Lock()
	box, err := srv.Svc.app.K.Box(10, 20, 30)
	srv.Svc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "b.stp")
	if err := box.ExportSTEP(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	code, out := post(t, ts.URL, srv.Token, "/api/step?name=block.stp&placement=center", data)
	if code != 200 || out["ok"] != true {
		t.Fatalf("import: %d %v", code, out)
	}
	srv.Svc.mu.Lock()
	node := srv.Svc.app.Graph.Get("block")
	srv.Svc.mu.Unlock()
	if node == nil || node.Type != "import_step" || node.Parameters["placement"] != "center" || node.Parameters["source"] != "block.stp" {
		t.Fatalf("node: %+v", node)
	}
}

func cube3MFBytes(t *testing.T, unit string, s int) []byte {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`<model unit="` + unit + `" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"><resources><object id="1" type="model"><mesh><vertices>`)
	for _, c := range [][3]int{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}, {0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1}} {
		sb.WriteString(fmt.Sprintf(`<vertex x="%d" y="%d" z="%d"/>`, c[0]*s, c[1]*s, c[2]*s))
	}
	sb.WriteString(`</vertices><triangles>`)
	for _, f := range [][4]int{{0, 3, 2, 1}, {4, 5, 6, 7}, {0, 1, 5, 4}, {1, 2, 6, 5}, {2, 3, 7, 6}, {3, 0, 4, 7}} {
		sb.WriteString(fmt.Sprintf(`<triangle v1="%d" v2="%d" v3="%d"/><triangle v1="%d" v2="%d" v3="%d"/>`, f[0], f[1], f[2], f[0], f[2], f[3]))
	}
	sb.WriteString(`</triangles></mesh></object></resources><build><item objectid="1"/></build></model>`)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("3D/3dmodel.model")
	w.Write([]byte(sb.String()))
	zw.Close()
	return buf.Bytes()
}

func Test3MFUploadImportsInMillimetres(t *testing.T) {
	srv, ts := testServer(t)
	// units in the query are ignored: the 3MF says centimeter itself.
	code, out := post(t, ts.URL, srv.Token, "/api/mesh?name=box.3mf&units=in", cube3MFBytes(t, "centimeter", 2))
	if code != 200 || out["ok"] != true {
		t.Fatalf("import: %d %v", code, out)
	}
	srv.Svc.mu.Lock()
	node := srv.Svc.app.Graph.Get("box")
	srv.Svc.mu.Unlock()
	if node == nil || node.Type != "import_mesh" || node.Parameters["source"] != "box.3mf" {
		t.Fatalf("node: %+v", node)
	}
	if u, _ := node.Parameters["units"].(string); u != "" && u != "mm" {
		t.Fatalf("units %v, want mm", u)
	}
	if code, out := post(t, ts.URL, srv.Token, "/api/mesh?name=bad.3mf", []byte("PK\x03\x04junk")); code != 400 || !strings.Contains(out["error"].(string), "3MF") {
		t.Fatalf("damaged 3MF: %d %v", code, out)
	}
}

func TestOBJUploadImportsWithUnits(t *testing.T) {
	srv, ts := testServer(t)
	obj := "v 0 0 0\nv 1 0 0\nv 1 1 0\nv 0 1 0\nv 0 0 1\nv 1 0 1\nv 1 1 1\nv 0 1 1\n" +
		"f 1 4 3 2\nf 5 6 7 8\nf 1 2 6 5\nf 2 3 7 6\nf 3 4 8 7\nf 4 1 5 8\n"
	code, out := post(t, ts.URL, srv.Token, "/api/mesh?name=box.obj&units=cm", []byte(obj))
	if code != 200 || out["ok"] != true {
		t.Fatalf("import: %d %v", code, out)
	}
	srv.Svc.mu.Lock()
	node := srv.Svc.app.Graph.Get("box")
	srv.Svc.mu.Unlock()
	if node == nil || node.Type != "import_mesh" || node.Parameters["source"] != "box.obj" || node.Parameters["units"] != "cm" {
		t.Fatalf("node: %+v", node)
	}
	if code, out := post(t, ts.URL, srv.Token, "/api/mesh?name=bad.obj", []byte("v 0 0 0\nf 1 2 3\n")); code != 400 {
		t.Fatalf("bad OBJ: %d %v", code, out)
	}
}
