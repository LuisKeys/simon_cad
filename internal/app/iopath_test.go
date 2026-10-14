package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveIOPathConfinesToProjectAndOutputDirs(t *testing.T) {
	a := &App{dir: t.TempDir()}
	if _, err := a.resolveIOPath(filepath.Join(a.dir, "out", "p.step"), true); err != nil {
		t.Fatalf("inside the project: %v", err)
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".bashrc"), "/etc/passwd", "../../../../etc/x", ""} {
		if _, err := a.resolveIOPath(p, true); err == nil {
			t.Errorf("%q must be refused", p)
		}
	}
	// a symlink inside the project that points outside must not be a way out
	link := filepath.Join(a.dir, "esc")
	if err := os.Symlink(home, link); err == nil {
		if _, err := a.resolveIOPath(filepath.Join(link, "x.stl"), true); err == nil {
			t.Error("symlink escape must be refused")
		}
	}
}
