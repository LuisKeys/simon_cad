package app

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Geometry must rebuild from the CAD-IR alone: the packages that build it may never depend
// on image interpretation or on any agent/LLM code.
func TestGeometryNeverImportsVisionOrAgent(t *testing.T) {
	forbidden := []string{"/internal/vision", "/internal/agent"}
	roots := []string{"../geometry", "../cadir", "../selectors", "../commands", "../persistence"}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				for _, bad := range forbidden {
					if strings.Contains(imp.Path.Value, bad) {
						t.Errorf("%s imports %s: geometry must not depend on the vision model or the agent", path, imp.Path.Value)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
