package config

import (
	"path/filepath"
	"testing"
)

func TestViewportDefaultsAndPersistence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	want := ViewportSettings{ShowAxes: true, ShowLabels: true, AxisOpacity: 70,
		ShowGizmo: true, GizmoSize: GizmoMedium, GizmoPosition: GizmoBottomLeft}
	cur := Current()
	if cur.Viewport == nil || *cur.Viewport != want {
		t.Fatalf("defaults: %+v", cur.Viewport)
	}
	// Each switch is independent, and "off" survives a reload instead of falling back to on.
	next := ViewportSettings{ShowAxes: false, ShowLabels: true, AxisOpacity: 0,
		ShowGizmo: true, GizmoSize: GizmoLarge, GizmoPosition: GizmoTopLeft}
	cur.Viewport = &next
	if err := Save(cur); err != nil {
		t.Fatal(err)
	}
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	if got := Current().Viewport; *got != next {
		t.Fatalf("not persisted: %+v", got)
	}
	// A save without viewport preferences leaves them alone.
	cur = Current()
	cur.Viewport = nil
	if err := Save(cur); err != nil {
		t.Fatal(err)
	}
	if got := Current().Viewport; *got != next {
		t.Fatalf("nil viewport reset the preferences: %+v", got)
	}
}

func TestViewportRejectsBadValues(t *testing.T) {
	_ = LoadFrom("")
	for _, bad := range []ViewportSettings{
		{AxisOpacity: 101, GizmoSize: GizmoSmall, GizmoPosition: GizmoTopLeft},
		{AxisOpacity: 50, GizmoSize: "huge", GizmoPosition: GizmoTopLeft},
		{AxisOpacity: 50, GizmoSize: GizmoSmall, GizmoPosition: "center"},
	} {
		s := Current()
		s.Viewport = &bad
		if Save(s) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
