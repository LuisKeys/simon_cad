package persistence

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/workspace"
)

// Every schema version this build has ever written must still load. The bump to v3 is the
// place this is easiest to get wrong: the migration switch has to name each older version
// explicitly, and omitting one strands every project saved by the previous release.
func TestEverySupportedSchemaVersionLoads(t *testing.T) {
	for v := 1; v <= SchemaVersion; v++ {
		dir := filepath.Join(t.TempDir(), "p.scadproj")
		if err := Save(dir, &Project{Meta: Meta{Name: "p", Units: "mm"}, Graph: cadir.NewGraph()}); err != nil {
			t.Fatal(err)
		}
		// Rewrite project.json as that older version would have been written.
		meta := fmt.Sprintf(`{
  "schema_version": %d,
  "name": "p",
  "units": "mm",
  "occt_version": "7.8.1",
  "created": "2026-01-02T03:04:05Z"
}
`, v)
		if err := os.WriteFile(filepath.Join(dir, "project.json"), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(dir)
		if err != nil {
			t.Fatalf("schema v%d: %v", v, err)
		}
		if p.Meta.ID == "" {
			t.Fatalf("schema v%d: a project without an id should get a stable one on load", v)
		}
		// Deriving it twice gives the same id, so re-migrating never churns the bytes.
		again, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if again.Meta.ID != p.Meta.ID {
			t.Fatalf("schema v%d: derived id is not stable (%s vs %s)", v, p.Meta.ID, again.Meta.ID)
		}
	}
}

// A project from a future version is refused rather than misread.
func TestNewerSchemaIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	if err := Save(dir, &Project{Meta: Meta{Name: "p", Units: "mm"}, Graph: cadir.NewGraph()}); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"schema_version": %d, "name": "p", "units": "mm", "occt_version": "7.8.1"}`, SchemaVersion+1)
	if err := os.WriteFile(filepath.Join(dir, "project.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("a newer schema version must be refused")
	}
}

// Workspace settings are saved with the project, canonical (mm, degrees) and versioned.
func TestWorkspaceSettingsRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	ws := workspace.Defaults()
	ws.Units.Linear, ws.Grid.Plane, ws.Grid.Spacing = workspace.Inch, workspace.PlaneXZ, 25.4
	inc := 5.0
	ws.Snap.Enabled, ws.Snap.TranslationIncrement = true, &inc
	if err := Save(dir, &Project{Meta: Meta{Name: "p", Units: "mm"}, Graph: cadir.NewGraph(), Workspace: &ws}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, WorkspaceFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"schema_version": 1`, `"linear": "in"`, `"spacing": 25.4`, `"translation_increment": 5`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("workspace.json lacks %s:\n%s", want, b)
		}
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Workspace == nil || !p.Workspace.Equal(ws) || len(p.WorkspaceWarnings) > 0 {
		t.Fatalf("loaded %+v (%v)", p.Workspace, p.WorkspaceWarnings)
	}
	// Byte-stable: saving what was loaded writes the same bytes.
	if err := Save(dir, p); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, WorkspaceFile)); string(again) != string(b) {
		t.Fatalf("not byte-stable:\n%s\nvs\n%s", b, again)
	}
}

// A project saved before workspace.json existed loads with no settings of its own; a
// damaged file loads with defaults for what cannot be read, and says so.
func TestWorkspaceBackwardCompatibility(t *testing.T) {
	p, err := Load(filepath.Join("..", "..", "testdata", "robot", "servo_bracket.scadproj"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Workspace != nil {
		t.Fatalf("a v1 project has no workspace settings, got %+v", p.Workspace)
	}
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	if err := Save(dir, &Project{Meta: Meta{Name: "p", Units: "mm"}, Graph: cadir.NewGraph()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, WorkspaceFile), []byte(`{"units":{"linear":"cm"},"grid":{"spacing":0},"extra":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Workspace.Units.Linear != workspace.CM || p.Workspace.Grid.Spacing != 10 || len(p.WorkspaceWarnings) != 1 {
		t.Fatalf("damaged file: %+v %v", p.Workspace, p.WorkspaceWarnings)
	}
	if err := os.WriteFile(filepath.Join(dir, WorkspaceFile), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err = Load(dir); err != nil || !p.Workspace.Equal(workspace.Defaults()) || len(p.WorkspaceWarnings) != 1 {
		t.Fatalf("unreadable file must load with defaults: %v %+v", err, p)
	}
}
