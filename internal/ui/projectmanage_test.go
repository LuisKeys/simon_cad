package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenameAndDeleteProject(t *testing.T) {
	t.Chdir(t.TempDir())
	s := newSvc(t)
	for _, n := range []string{"a", "b"} {
		if _, err := s.Save(n); err != nil {
			t.Fatal(err)
		}
	}
	// b is the open project now: it cannot be renamed or deleted from under the session.
	if _, err := s.RenameProject(filepath.Join(OutputDir, "b.scadproj"), "x"); err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("renaming the open project: %v", err)
	}
	if err := s.DeleteProject(filepath.Join(OutputDir, "b.scadproj")); err == nil {
		t.Fatal("deleted the open project")
	}
	a := filepath.Join(OutputDir, "a.scadproj")
	if _, err := s.RenameProject(a, "b"); err == nil {
		t.Fatal("renamed over an existing project")
	}
	for _, bad := range []string{"", "../x", "x/y"} {
		if _, err := s.RenameProject(a, bad); err == nil {
			t.Fatalf("accepted name %q", bad)
		}
	}
	to, err := s.RenameProject(a, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(to, "project.json"))
	if !strings.Contains(string(b), `"name": "renamed"`) {
		t.Fatalf("project.json keeps the old name:\n%s", b)
	}
	if err := s.DeleteProject("../outside.scadproj"); err == nil {
		t.Fatal("deleted outside the projects folder")
	}
	if err := s.DeleteProject(to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(to); !os.IsNotExist(err) {
		t.Fatal("project still on disk")
	}
	if got := s.ListProjects(); len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("list: %+v", got)
	}
}
