package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ioRoots are the directories a tool may read from or write to on the model's behalf: the
// open project, the output directories under the working directory, and the OS temp dir.
func (a *App) ioRoots() []string {
	var roots []string
	if a.dir != "" {
		roots = append(roots, a.dir)
	}
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, filepath.Join(wd, "projects_output"), filepath.Join(wd, "out"))
	}
	return append(roots, os.TempDir())
}

// resolveIOPath makes path absolute and checks that, symlinks resolved, it lies inside one of
// the allowed roots. Writes may name a file that does not exist yet (and new directories).
func (a *App) resolveIOPath(path string, write bool) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real := evalExisting(abs)
	for _, r := range a.ioRoots() {
		ra, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if insideDir(evalExisting(ra), real) {
			return abs, nil
		}
	}
	verb := "read"
	if write {
		verb = "write"
	}
	return "", fmt.Errorf("refusing to %s %s: tools may only use files inside the project directory, projects_output/, out/ or the temp directory", verb, path)
}

// evalExisting resolves symlinks in the longest existing prefix of p and re-appends the rest.
func evalExisting(p string) string {
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func insideDir(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
