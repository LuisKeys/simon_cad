package persistence

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/accelone/simoncad/internal/cadir"
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
