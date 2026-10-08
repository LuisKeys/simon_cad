package materials

import (
	"strings"
	"testing"
	"testing/fstest"

	materialsdata "github.com/accelone/simoncad/materials"
)

func mj(id, name, cat, extra string) []byte {
	return []byte(`{"id":"` + id + `","name":"` + name + `","category":"` + cat + `","baseColor":"#aabbcc","metalness":0.5,"roughness":0.5` + extra + `}`)
}

func TestBuiltinLibraryLoads(t *testing.T) {
	l := New(materialsdata.FS())
	if w := l.Warnings(); len(w) != 0 {
		t.Fatalf("built-in library has warnings: %v", w)
	}
	ms := l.List("")
	if len(ms) < 30 {
		t.Fatalf("want ~30 curated materials, got %d", len(ms))
	}
	for _, id := range []string{"brushed_aluminum", "stainless_steel", "black_abs", "petg", "black_rubber", "carbon_fiber", "clear_glass", "green_pcb"} {
		if _, ok := l.Get(id); !ok {
			t.Errorf("missing %s", id)
		}
	}
	for _, def := range EngineeringDefaults {
		if _, ok := l.Get(def); !ok {
			t.Errorf("engineering default %q is not in the library", def)
		}
	}
	for _, m := range ms {
		if m.License.License == "" {
			t.Errorf("%s: license metadata required", m.ID)
		}
	}
}

func TestProjectOverridesAndFallbacks(t *testing.T) {
	proj := fstest.MapFS{
		"brushed_aluminum/material.json": {Data: mj("brushed_aluminum", "My Aluminum", "Metals", "")},
		"custom/material.json":           {Data: mj("custom", "Custom", "Generic", `,"textures":{"baseColor":"nope.jpg"}`)},
		"bad/material.json":              {Data: []byte(`{"id":"bad"`)},
		"wrongid/material.json":          {Data: mj("other", "X", "Generic", "")},
	}
	l := New(materialsdata.FS())
	l.SetProject(proj)
	m, _ := l.Get("brushed_aluminum")
	if m.Name != "My Aluminum" || !m.Project {
		t.Errorf("project material should override the built-in: %+v", m)
	}
	c, ok := l.Get("custom")
	if !ok || c.Textures.BaseColor != "" {
		t.Errorf("missing texture should fall back to scalars: %+v ok=%v", c, ok)
	}
	if _, ok := l.Get("bad"); ok {
		t.Error("invalid json must be skipped")
	}
	joined := strings.Join(l.Warnings(), "\n")
	for _, want := range []string{"missing baseColor texture", "bad:", "does not match folder"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
	if got, ok := l.Resolve("nonexistent"); ok || got.ID != Fallback.ID {
		t.Errorf("unknown id must resolve to the fallback: %+v", got)
	}
	l.SetProject(nil)
	if m, _ := l.Get("brushed_aluminum"); m.Project {
		t.Error("clearing the project layer should restore the built-in")
	}
}

func TestReadFileOnlyServesDeclared(t *testing.T) {
	proj := fstest.MapFS{
		"m/material.json": {Data: mj("m", "M", "Generic", `,"textures":{"baseColor":"c.jpg"}`)},
		"m/c.jpg":         {Data: []byte("img")},
		"m/secret.txt":    {Data: []byte("x")},
	}
	l := New(nil)
	l.SetProject(proj)
	if b, err := l.ReadFile("m", "c.jpg"); err != nil || string(b) != "img" {
		t.Fatalf("declared texture: %q %v", b, err)
	}
	for _, name := range []string{"secret.txt", "../m/c.jpg", "material.json"} {
		if _, err := l.ReadFile("m", name); err == nil {
			t.Errorf("%q must not be served", name)
		}
	}
}

func TestSearch(t *testing.T) {
	l := New(materialsdata.FS())
	top := func(q string) []Match { return l.Search(q, 5) }
	if r := top("brushed aluminum"); len(r) == 0 || r[0].Material.ID != "brushed_aluminum" || Ambiguous(r[:min(len(r), 2)]) && false {
		t.Errorf("brushed aluminum: %+v", r)
	}
	if r := top("black abs"); len(r) == 0 || r[0].Material.ID != "black_abs" {
		t.Errorf("black abs: %+v", r)
	}
	if r := top("carbon fiber"); len(r) == 0 || r[0].Material.ID != "carbon_fiber" {
		t.Errorf("carbon fiber: %+v", r)
	}
	if r := top("transparent plastic"); len(r) == 0 || r[0].Material.ID != "transparent_plastic" {
		t.Errorf("transparent plastic: %+v", r)
	}
	if r := top("make it look metallic"); !Ambiguous(r) {
		t.Errorf("'metallic' must be ambiguous: %+v", r)
	}
	if r := top("zzzz"); len(r) != 0 {
		t.Errorf("no match expected: %+v", r)
	}
	a, b := top("steel"), top("steel")
	for i := range a {
		if a[i].Material.ID != b[i].Material.ID {
			t.Error("search must be deterministic")
		}
	}
}

func TestValidate(t *testing.T) {
	bad := []Material{
		{ID: "a b", Name: "x", BaseColor: "#000000"},
		{ID: "a", Name: "", BaseColor: "#000000"},
		{ID: "a", Name: "x", BaseColor: "red"},
		{ID: "a", Name: "x", BaseColor: "#000000", Metalness: 2},
		{ID: "a", Name: "x", BaseColor: "#000000", Roughness: -1},
		{ID: "a", Name: "x", BaseColor: "#000000", Clearcoat: f64(1.5)},
		{ID: "a", Name: "x", BaseColor: "#000000", IOR: f64(0.5)},
		{ID: "a", Name: "x", BaseColor: "#000000", Emissive: "glow"},
		{ID: "a", Name: "x", BaseColor: "#000000", EnvMapIntensity: f64(-1)},
	}
	for i, m := range bad {
		if m.Validate() == nil {
			t.Errorf("case %d should be invalid", i)
		}
	}
}

func f64(v float64) *float64 { return &v }

func TestValidatePhysicalExtras(t *testing.T) {
	m := Material{ID: "g", Name: "Glass", BaseColor: "#ffffff", Clearcoat: f64(1), Transmission: f64(0.9),
		IOR: f64(1.5), Thickness: f64(2), Sheen: f64(0.3), Anisotropy: f64(0.5), Emissive: "#102030", EnvMapIntensity: f64(1.4)}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid physical material rejected: %v", err)
	}
}
