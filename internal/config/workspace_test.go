package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/accelone/simoncad/internal/workspace"
)

func TestWorkspaceDefaultsPersistence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	if got := Current().Workspace; got == nil || !got.Equal(workspace.Defaults()) {
		t.Fatalf("factory defaults: %+v", got)
	}
	ws := workspace.Defaults()
	ws.Units.Linear, ws.Grid.Spacing, ws.Snap.Enabled = workspace.Inch, 25.4, true
	cur := Current()
	cur.Workspace = &ws
	if err := Save(cur); err != nil {
		t.Fatal(err)
	}
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	if got := WorkspaceDefaults(); !got.Equal(ws) {
		t.Fatalf("not persisted: %+v", got)
	}
	// A client that does not send them leaves them alone.
	cur = Current()
	cur.Workspace = nil
	if err := Save(cur); err != nil {
		t.Fatal(err)
	}
	if got := WorkspaceDefaults(); !got.Equal(ws) {
		t.Fatalf("nil workspace reset the defaults: %+v", got)
	}
	// Invalid defaults are refused and leave the saved ones in place.
	bad := ws.Clone()
	bad.Grid.Spacing = 0
	cur.Workspace = &bad
	if Save(cur) == nil {
		t.Fatal("accepted a zero grid spacing")
	}
	if got := WorkspaceDefaults(); !got.Equal(ws) {
		t.Fatalf("a refused save changed the defaults: %+v", got)
	}
}

func TestWorkspaceDefaultsTolerateAHandEditedFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(file, []byte(`{"SIMONCAD_WORKSPACE_DEFAULTS": "{\"units\":{\"linear\":\"cm\"},\"grid\":{\"spacing\":-3}}"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadFrom(file); err != nil {
		t.Fatal(err)
	}
	got := WorkspaceDefaults()
	if got.Units.Linear != workspace.CM || got.Grid.Spacing != 10 {
		t.Fatalf("hand-edited defaults: %+v", got)
	}
	_ = LoadFrom("")
}
