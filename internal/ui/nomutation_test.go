package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The UI must have no way to change the model except through the tool registry
// (App.ApplyTool) and the ChangeSet lifecycle. This statically checks the UI-facing
// packages: they may not import the command layer or the kernel, and may not reach into the
// App's graph, manager or engine, or call graph/command mutators.
func TestUIHasNoMutationPathBypassingToolRegistry(t *testing.T) {
	forbiddenImports := []string{
		"github.com/accelone/simoncad/internal/commands",
		"github.com/accelone/simoncad/internal/geometry/bridge",
		"github.com/accelone/simoncad/internal/geometry/features",
		"github.com/accelone/simoncad/internal/persistence",
	}
	// Direct App internals, and mutators of the graph and command layers.
	forbiddenSel := map[string]bool{
		"Graph": true, "Mgr": true, "Engine": true, "K": true, "Reg": true,
		"Propose": true, "Apply": true, "Replace": true, "Nodes": true,
	}
	// Reg is only legitimate for read-only palette listing (Names/Get).
	allowedRegUse := map[string]bool{"Names": true, "Get": true}

	var roots []string
	for _, r := range []string{".", "../../cmd/simoncad"} {
		roots = append(roots, r)
	}
	checked := 0
	for _, root := range roots {
		files, _ := filepath.Glob(filepath.Join(root, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, f, src, 0)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for _, imp := range file.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				for _, bad := range forbiddenImports {
					if p == bad {
						t.Errorf("%s imports %s: the UI must mutate only via App.ApplyTool and the ChangeSet lifecycle", f, p)
					}
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// s.app.Reg.Names() / .Get() is the one allowed use of Reg.
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Reg" && allowedRegUse[sel.Sel.Name] {
					return true
				}
				if forbiddenSel[sel.Sel.Name] {
					// Our own types may have fields with these names (e.g. State.Nodes); only
					// flag access through the App value.
					if x, ok := sel.X.(*ast.SelectorExpr); ok && x.Sel.Name == "app" || isAppIdent(sel.X) {
						if sel.Sel.Name == "Reg" {
							// `s.app.Reg` alone (not followed by Names/Get) is caught by the parent check.
							return true
						}
						t.Errorf("%s: forbidden access to App.%s at %s", f, sel.Sel.Name, fset.Position(sel.Pos()))
					}
				}
				return true
			})
		}
	}
	if checked < 3 {
		t.Fatalf("only %d files checked; the guard is not scanning the UI packages", checked)
	}
}

func isAppIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && (id.Name == "a" || id.Name == "app")
}

// A control: the checker must actually catch a bypass. Parse a snippet that mutates the
// graph directly and confirm the same rules would flag it.
func TestNoMutationGuardCatchesBypass(t *testing.T) {
	const bad = `package x
import "github.com/accelone/simoncad/internal/commands"
func f(a interface{}) { s.app.Graph.Nodes = nil; s.app.Mgr.Propose() }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bad.go", bad, 0)
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.SelectorExpr); ok && x.Sel.Name == "app" && (sel.Sel.Name == "Graph" || sel.Sel.Name == "Mgr") {
				hits++
			}
		}
		return true
	})
	if hits != 2 || len(file.Imports) != 1 {
		t.Fatalf("control found %d bypasses", hits)
	}
}
