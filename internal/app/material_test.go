package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/rendering"
)

func twoBoxes(t *testing.T) *App {
	a := New("demo", clock)
	t.Cleanup(a.Close)
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":80,"dy":60,"dz":4}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"lid","dx":10,"dy":10,"dz":4}`), PolicyAuto))
	return a
}

func TestMaterialAssignRemoveUndoRedo(t *testing.T) {
	a := twoBoxes(t)
	m, _ := a.Model()
	rend := rendering.New()
	d1, _ := rend.Scene(m, nil)
	misses := a.Engine.Stats.Misses

	// multi-selection assignment is one ChangeSet
	if err := a.SetSelection([]string{"body(feature=plate)", "body(feature=lid)"}); err != nil {
		t.Fatal(err)
	}
	o := mustOK(t, a.ApplyTool("set_material", []byte(`{"material_id":"brushed_aluminum"}`), PolicyAuto))
	if !o.Committed || len(o.ChangeSet.Commands) != 2 || !o.ChangeSet.AppearanceOnly() {
		t.Fatalf("material assignment should be one auto-committed appearance ChangeSet: %+v", o)
	}
	for _, id := range []string{"plate", "lid"} {
		if ap := a.Graph.Get(id).Appearance; ap == nil || ap.MaterialID != "brushed_aluminum" {
			t.Fatalf("%s: %+v", id, ap)
		}
	}
	// color override, opacity and scale layer on top and don't drop the material
	mustOK(t, a.ApplyTool("set_color", []byte(`{"features":["plate"],"color":"#ff0000"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_opacity", []byte(`{"features":["plate"],"opacity":0.4}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_material_scale", []byte(`{"features":["plate"],"scale":1.5}`), PolicyAuto))
	ap := a.Graph.Get("plate").Appearance
	if ap.MaterialID != "brushed_aluminum" || ap.Color != "#FF0000" || *ap.Opacity != 0.4 || *ap.TextureScale != 1.5 {
		t.Fatalf("layered appearance: %+v", ap)
	}
	mustOK(t, a.ApplyTool("remove_material", []byte(`{"features":["plate"]}`), PolicyAuto))
	if a.Graph.Get("plate").Appearance.MaterialID != "" || a.Graph.Get("plate").Appearance.Color != "#FF0000" {
		t.Fatalf("remove_material must only clear the material: %+v", a.Graph.Get("plate").Appearance)
	}

	// no geometry work for any of it
	m2, _ := a.Model()
	d2, _ := rend.Scene(m2, d1.Keys)
	if a.Engine.Stats.Misses != misses || d2.Stats.Tessellated != 0 || len(d2.Changed) != 0 {
		t.Errorf("material edits touched the kernel: misses %d->%d tess=%d", misses, a.Engine.Stats.Misses, d2.Stats.Tessellated)
	}

	// undo x5 → pristine; redo x1 → material back
	for i := 0; i < 5; i++ {
		if _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"plate", "lid"} {
		if a.Graph.Get(id).Appearance != nil {
			t.Errorf("%s not pristine after undo: %+v", id, a.Graph.Get(id).Appearance)
		}
	}
	if _, err := a.Redo(); err != nil {
		t.Fatal(err)
	}
	if a.Graph.Get("lid").Appearance == nil || a.Graph.Get("lid").Appearance.MaterialID != "brushed_aluminum" {
		t.Error("redo should restore the material")
	}
}

func TestMaterialUnknownIsRejectedWithHint(t *testing.T) {
	a := twoBoxes(t)
	o := a.ApplyTool("set_material", []byte(`{"features":["plate"],"material_id":"aluminum_brush"}`), PolicyAuto)
	if o.OK || !strings.Contains(o.Error, "aluminum") {
		t.Fatalf("unknown material id must be rejected with a hint: %+v", o)
	}
	if a.Graph.Get("plate").Appearance != nil {
		t.Error("rejected call must not change the model")
	}
}

func TestMaterialSearchToolAmbiguity(t *testing.T) {
	a := twoBoxes(t)
	out := mustOK(t, a.ApplyTool("search_materials", []byte(`{"query":"make it look metallic"}`), PolicyAuto)).Data
	if s := toJSON(out); !contains(s, `"ambiguous":true`) {
		t.Errorf("metallic should be ambiguous: %s", s)
	}
	out = mustOK(t, a.ApplyTool("search_materials", []byte(`{"query":"carbon fiber"}`), PolicyAuto)).Data
	if s := toJSON(out); !contains(s, `"ambiguous":false`) || !contains(s, `"carbon_fiber"`) {
		t.Errorf("carbon fiber should be a clear match: %s", s)
	}
}

func TestMaterialPersistsByReferenceAndProjectMaterials(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := twoBoxes(t)
	mustOK(t, a.ApplyTool("set_material", []byte(`{"features":["plate"],"material_id":"black_abs"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_material_scale", []byte(`{"features":["plate"],"scale":2}`), PolicyAuto))
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	first := snapshot(t, dir)
	if _, err := os.Stat(filepath.Join(dir, "assets", "materials")); err == nil {
		t.Error("global materials must not be copied into the project")
	}
	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ap := b.Graph.Get("plate").Appearance
	if ap == nil || ap.MaterialID != "black_abs" || *ap.TextureScale != 2 {
		t.Fatalf("not persisted: %+v", ap)
	}
	dir2 := filepath.Join(t.TempDir(), "q.scadproj")
	if err := b.Save(dir2); err != nil {
		t.Fatal(err)
	}
	for n, c := range first {
		if snapshot(t, dir2)[n] != c {
			t.Errorf("%s differs after load→save", n)
		}
	}

	// project-specific material: loaded from assets/materials, carried over on "save as"
	pm := filepath.Join(dir, "assets", "materials", "shop_paint")
	if err := os.MkdirAll(pm, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(pm, "material.json"), []byte(`{"id":"shop_paint","name":"Shop Paint","category":"Generic","baseColor":"#336699","metalness":0,"roughness":0.6}`), 0o644)
	c, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	mustOK(t, c.ApplyTool("set_material", []byte(`{"features":["lid"],"material_id":"shop_paint"}`), PolicyAuto))
	dir3 := filepath.Join(t.TempDir(), "r.scadproj")
	if err := c.Save(dir3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir3, "assets", "materials", "shop_paint", "material.json")); err != nil {
		t.Errorf("project material not carried to the new location: %v", err)
	}
}

func TestMissingMaterialFallsBackAtRender(t *testing.T) {
	// A project may reference a material that no longer exists: loading must still work and
	// the id must be kept (so restoring the library fixes it).
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := twoBoxes(t)
	mustOK(t, a.ApplyTool("set_material", []byte(`{"features":["plate"],"material_id":"black_abs"}`), PolicyAuto))
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(dir, "model.json")
	raw, _ := os.ReadFile(mp)
	os.WriteFile(mp, []byte(replaceAll(string(raw), "black_abs", "gone_material")), 0o644)
	b, err := Open(dir, clock)
	if err != nil {
		t.Fatalf("missing material must not fail loading: %v", err)
	}
	defer b.Close()
	if b.Graph.Get("plate").Appearance.MaterialID != "gone_material" {
		t.Error("dangling reference should be preserved")
	}
	if m, ok := b.Materials.Resolve("gone_material"); ok || m.ID != "generic_gray" {
		t.Errorf("resolve fallback: %+v", m)
	}
}

func toJSON(v any) string              { b, _ := json.Marshal(v); return string(b) }
func contains(s, sub string) bool      { return strings.Contains(s, sub) }
func replaceAll(s, a, b string) string { return strings.ReplaceAll(s, a, b) }

// Assigning a material replaces an earlier color override (so the material's color shows),
// and undo restores both the old color and the old material.
func TestSetMaterialClearsColorOverride(t *testing.T) {
	a := twoBoxes(t)
	mustOK(t, a.ApplyTool("set_color", []byte(`{"features":["plate"],"color":"#ff0000"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_material", []byte(`{"features":["plate"],"material_id":"brushed_aluminum"}`), PolicyAuto))
	if ap := a.Graph.Get("plate").Appearance; ap.MaterialID != "brushed_aluminum" || ap.Color != "" {
		t.Fatalf("color override should be cleared: %+v", ap)
	}
	mustOK(t, a.ApplyTool("set_material", []byte(`{"features":["plate"],"material_id":"black_abs","keep_color":true}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_color", []byte(`{"features":["plate"],"color":"#00ff00"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_material", []byte(`{"features":["plate"],"material_id":"petg","keep_color":true}`), PolicyAuto))
	if ap := a.Graph.Get("plate").Appearance; ap.Color != "#00FF00" {
		t.Fatalf("keep_color should preserve the override: %+v", ap)
	}
	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if ap := a.Graph.Get("plate").Appearance; ap.MaterialID != "black_abs" || ap.Color != "#00FF00" {
		t.Fatalf("undo should restore material and color: %+v", ap)
	}
}
