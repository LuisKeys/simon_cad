package app

import (
	"path/filepath"
	"strings"
	"testing"
)

func savedProject(t *testing.T, occt string) string {
	t.Helper()
	a := New("p", clock)
	defer a.Close()
	if o := a.ApplyTool("create_box", []byte(`{"id":"a","dx":10,"dy":10,"dz":10}`), PolicyCommit); !o.OK {
		t.Fatal(o.Error)
	}
	if occt != "" {
		a.Meta.OCCTVersion = occt
	}
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A project saved with another OpenCASCADE version still opens, with a note that its geometry
// may differ: the determinism contract is only pinned for the version that wrote it.
func TestOpenWarnsWhenSavedWithAnotherOCCTVersion(t *testing.T) {
	a, err := Open(savedProject(t, "6.9.9"), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.OpenWarnings) != 1 || !strings.Contains(a.OpenWarnings[0], "6.9.9") || !strings.Contains(a.OpenWarnings[0], a.K.Version()) {
		t.Fatalf("warnings = %q", a.OpenWarnings)
	}
	if a.Graph.Get("a") == nil {
		t.Fatal("the project must still load")
	}
}

func TestOpenDoesNotWarnForTheSameVersion(t *testing.T) {
	a, err := Open(savedProject(t, ""), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.OpenWarnings) != 0 {
		t.Fatalf("unexpected warnings: %q", a.OpenWarnings)
	}
}
