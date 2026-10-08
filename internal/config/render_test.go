package config

import (
	"path/filepath"
	"testing"
)

func TestRenderDefaultsAndPersistence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	cur := Current()
	if cur.Render == nil || *cur.Render != DefaultRender {
		t.Fatalf("defaults: %+v", cur.Render)
	}
	next := RenderSettings{Environment: EnvRoom, EnvIntensity: 150, Exposure: 80, ToneMapping: ToneACES, Shadows: ShadowsOff}
	cur.Render = &next
	if err := Save(cur); err != nil {
		t.Fatal(err)
	}
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	if got := Current().Render; *got != next {
		t.Fatalf("not persisted: %+v", got)
	}
	cur = Current()
	cur.Render = nil // a client that does not know the preferences leaves them alone
	if err := Save(cur); err != nil {
		t.Fatal(err)
	}
	if got := Current().Render; *got != next {
		t.Fatalf("nil render reset the preferences: %+v", got)
	}
}

func TestRenderRejectsBadValues(t *testing.T) {
	_ = LoadFrom("")
	ok := DefaultRender
	for _, mut := range []func(*RenderSettings){
		func(r *RenderSettings) { r.EnvIntensity = 301 },
		func(r *RenderSettings) { r.Exposure = 10 },
		func(r *RenderSettings) { r.Environment = "hdri" },
		func(r *RenderSettings) { r.ToneMapping = "reinhard" },
		func(r *RenderSettings) { r.Shadows = "ultra" },
	} {
		bad := ok
		mut(&bad)
		s := Current()
		s.Render = &bad
		if Save(s) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
