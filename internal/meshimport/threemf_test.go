package meshimport

import (
	"archive/zip"
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
)

// cubeObject writes a cube of side s (in file units) as a 3MF <object>.
func cubeObject(id int, s float64) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<object id="%d" type="model"><mesh><vertices>`, id)
	for _, c := range [][3]float64{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}, {0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1}} {
		fmt.Fprintf(&sb, `<vertex x="%g" y="%g" z="%g"/>`, c[0]*s, c[1]*s, c[2]*s)
	}
	sb.WriteString(`</vertices><triangles>`)
	for _, f := range [][4]int{{0, 3, 2, 1}, {4, 5, 6, 7}, {0, 1, 5, 4}, {1, 2, 6, 5}, {2, 3, 7, 6}, {3, 0, 4, 7}} {
		fmt.Fprintf(&sb, `<triangle v1="%d" v2="%d" v3="%d"/><triangle v1="%d" v2="%d" v3="%d"/>`, f[0], f[1], f[2], f[0], f[2], f[3])
	}
	sb.WriteString(`</triangles></mesh></object>`)
	return sb.String()
}

func model(unit, extra, resources, build string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><model unit="%s" xml:lang="en-US" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:m="http://schemas.microsoft.com/3dmanufacturing/material/2015/02" xmlns:x="urn:other" %s><resources>%s</resources><build>%s</build></model>`,
		unit, extra, resources, build)
}

func pack(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const rels = `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Target="/3D/part.model" Id="r0" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/></Relationships>`

func pack3MF(t *testing.T, m string) []byte {
	return pack(t, map[string]string{"_rels/.rels": rels, "3D/part.model": m})
}

func TestThreeMFUnitsAreConvertedToMillimetres(t *testing.T) {
	for unit, want := range map[string]float64{"millimeter": 10, "centimeter": 100, "inch": 254, "micron": 0.01, "meter": 10000} {
		data := pack3MF(t, model(unit, "", cubeObject(1, 10), `<item objectid="1"/>`))
		p, err := Prepare(data, Options{})
		if err != nil {
			t.Fatalf("%s: %v", unit, err)
		}
		if p.Report.Format != "3mf" || p.Report.FileUnit != unit {
			t.Fatalf("%s: report %+v", unit, p.Report)
		}
		if got := p.Report.Size()[0]; math.Abs(got-want) > 1e-3*want {
			t.Fatalf("%s: width %v mm, want %v", unit, got, want)
		}
	}
}

func TestThreeMFSameSolidAsSTL(t *testing.T) {
	stl, err := Prepare(binarySTL(cube(vec{}, 25.4)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// 1 inch cube in 3MF is the 25.4 mm cube: same canonical mesh, same content id.
	tmf, err := Prepare(pack3MF(t, model("inch", "", cubeObject(1, 1), `<item objectid="1"/>`)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stl.ID != tmf.ID {
		t.Fatalf("ids differ: %s vs %s", stl.ID, tmf.ID)
	}
}

func TestThreeMFComponentsTransformsAndMirror(t *testing.T) {
	// Object 2 is a component of object 1 moved by (100,0,0) and mirrored in x: the winding flips back, so it is still valid.
	res := cubeObject(1, 10) + `<object id="2"><components><component objectid="1" transform="-1 0 0 0 1 0 0 0 1 100 0 0"/></components></object>`
	p, err := Prepare(pack3MF(t, model("millimeter", "", res, `<item objectid="2" transform="1 0 0 0 1 0 0 0 1 0 5 0"/>`)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Report.Min != [3]float64{90, 5, 0} || p.Report.Max != [3]float64{100, 15, 10} {
		t.Fatalf("bounds %v %v", p.Report.Min, p.Report.Max)
	}
	if p.Report.Volume < 999 || p.Report.Volume > 1001 {
		t.Fatalf("volume %v", p.Report.Volume)
	}
}

func TestThreeMFRefusals(t *testing.T) {
	obj := cubeObject(1, 10)
	cases := map[string]struct {
		data []byte
		want string
	}{
		"damaged zip":    {append([]byte("PK\x03\x04"), make([]byte, 40)...), "3MF"},
		"no model part":  {pack(t, map[string]string{"a.txt": "x"}), "no 3D model part"},
		"not xml":        {pack3MF(t, "<model"), "malformed"},
		"empty build":    {pack3MF(t, model("millimeter", "", obj, "")), "empty build"},
		"missing object": {pack3MF(t, model("millimeter", "", obj, `<item objectid="9"/>`)), "object 9"},
		"bad unit":       {pack3MF(t, model("furlong", "", obj, `<item objectid="1"/>`)), "unknown unit"},
		"bad extension":  {pack3MF(t, model("millimeter", `requiredextensions="x"`, obj, `<item objectid="1"/>`)), "extension"},
		"external part":  {pack3MF(t, model("millimeter", "", obj+`<object id="2"><components><component objectid="1" p:path="/3D/o.model" xmlns:p="urn:p"/></components></object>`, `<item objectid="2"/>`)), "another model part"},
		"support object": {pack3MF(t, model("millimeter", "", strings.Replace(obj, `type="model"`, `type="support"`, 1), `<item objectid="1"/>`)), "not a printable solid"},
		"cycle":          {pack3MF(t, model("millimeter", "", `<object id="1"><components><component objectid="1"/></components></object>`, `<item objectid="1"/>`)), "themselves"},
		"bad vertex":     {pack3MF(t, model("millimeter", "", strings.Replace(obj, `v3="2"`, `v3="99"`, 1), `<item objectid="1"/>`)), "vertex 99"},
		"two cubes":      {pack3MF(t, model("millimeter", "", obj+strings.Replace(cubeObject(2, 10), `x="0"`, `x="50"`, -1), `<item objectid="1"/><item objectid="2"/>`)), ""},
	}
	for name, c := range cases {
		_, err := Prepare(c.data, Options{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, c.want)
		}
	}
}

func TestThreeMFZipBombIsRefused(t *testing.T) {
	big := strings.Repeat(" ", int(maxPartBytes)+10)
	if _, err := Prepare(pack3MF(t, big), Options{}); err == nil || !strings.Contains(err.Error(), "MB") {
		t.Fatalf("got %v", err)
	}
}

func TestThreeMFTriangleLimit(t *testing.T) {
	data := pack3MF(t, model("millimeter", "", cubeObject(1, 10), `<item objectid="1"/>`))
	if _, err := Prepare(data, Options{MaxTriangles: 5}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("got %v", err)
	}
}
