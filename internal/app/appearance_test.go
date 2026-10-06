package app

import (
	"path/filepath"
	"testing"

	"github.com/accelone/simoncad/internal/rendering"
)

// An appearance-only ChangeSet must not invoke the geometry kernel: no rebuild-cache
// misses, no new tessellation, and the mesh keys are unchanged.
func TestAppearanceChangeSetSkipsKernel(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":80,"dy":60,"dz":4}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"lid","dx":10,"dy":10,"dz":4}`), PolicyAuto))
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	rend := rendering.New()
	d1, err := rend.Scene(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	misses, tess := a.Engine.Stats.Misses, d1.Stats.Tessellated

	o := mustOK(t, a.ApplyTool(`set_color`, []byte(`{"features":["plate","lid"],"color":"#ff0000"}`), PolicyAuto))
	if !o.Committed || len(o.ChangeSet.Commands) != 2 {
		t.Fatalf("appearance edits should auto-commit as one ChangeSet: %+v", o)
	}
	mustOK(t, a.ApplyTool(`set_opacity`, []byte(`{"features":["plate"],"opacity":0.5}`), PolicyAuto))
	mustOK(t, a.ApplyTool(`set_visibility`, []byte(`{"features":["lid"],"visible":false}`), PolicyAuto))

	m2, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	d2, err := rend.Scene(m2, d1.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if a.Engine.Stats.Misses != misses {
		t.Errorf("appearance edits rebuilt geometry: misses %d -> %d", misses, a.Engine.Stats.Misses)
	}
	if d2.Stats.Tessellated != 0 || len(d2.Changed) != 0 {
		t.Errorf("appearance edits re-tessellated: %+v changed=%d", d2.Stats, len(d2.Changed))
	}
	if tess == 0 {
		t.Fatal("test setup: nothing was tessellated")
	}
	if len(o.ChangeSet.Effects.Modified) != 2 {
		t.Errorf("effects should list modified features: %+v", o.ChangeSet.Effects)
	}

	// undo x3 restores a pristine, appearance-free graph; redo-free replay matches.
	for i := 0; i < 3; i++ {
		if _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
	}
	if a.Graph.Get("plate").Appearance != nil || a.Graph.Get("lid").Appearance != nil {
		t.Errorf("undo did not restore defaults: %+v", a.Graph.Get("plate").Appearance)
	}
}

func TestAppearancePersistsAndReplays(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":80,"dy":60,"dz":4}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_color", []byte(`{"features":["plate"],"color":"#1e90ff"}`), PolicyAuto))
	mustOK(t, a.ApplyTool("set_opacity", []byte(`{"features":["plate"],"opacity":0.25}`), PolicyAuto))
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	first := snapshot(t, dir)
	a.Close()

	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ap := b.Graph.Get("plate").Appearance
	if ap == nil || ap.Color != "#1E90FF" || ap.Opacity == nil || *ap.Opacity != 0.25 {
		t.Fatalf("appearance not persisted: %+v", ap)
	}
	dir2 := filepath.Join(t.TempDir(), "q.scadproj")
	if err := b.Save(dir2); err != nil {
		t.Fatal(err)
	}
	for name, content := range first {
		if snapshot(t, dir2)[name] != content {
			t.Errorf("%s differs after load→save", name)
		}
	}
	if _, match, n, err := b.Replay(); err != nil || !match || n != 3 {
		t.Fatalf("replay: match=%v n=%d err=%v", match, n, err)
	}
	// get_appearance and selection-based targeting.
	if err := b.SetSelection([]string{"body(feature=plate)"}); err != nil {
		t.Fatal(err)
	}
	mustOK(t, b.ApplyTool("reset_appearance", []byte(`{}`), PolicyAuto))
	if b.Graph.Get("plate").Appearance != nil {
		t.Error("reset via selection did not clear appearance")
	}
}
