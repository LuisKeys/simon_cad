package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/app"
)

// ---- project --------------------------------------------------------------------------

// New starts an empty project.
func (s *Service) New(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if name == "" {
		name = "untitled"
	}
	s.attach(app.New(name, s.Clock), "")
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// Open loads a .scadproj directory.
func (s *Service) Open(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	dir = s.findProject(dir)
	a, err := app.Open(dir, s.Clock)
	if err != nil {
		return err
	}
	s.attach(a, dir)
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

const (
	projectExt = ".scadproj"
	// OutputDir is where projects saved with a relative path are written.
	OutputDir = "projects_output"
)

// resolveProjectPath places a bare or relative project path under OutputDir; absolute
// paths and paths already under OutputDir are kept as typed.
func resolveProjectPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "~") {
		return p
	}
	clean := filepath.Clean(p)
	if clean == OutputDir || strings.HasPrefix(clean, OutputDir+string(filepath.Separator)) {
		return clean
	}
	return filepath.Join(OutputDir, clean)
}

// withProjectExt restores the .scadproj extension when the user typed a bare name.
func withProjectExt(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || strings.HasSuffix(strings.ToLower(strings.TrimRight(p, "/")), projectExt) {
		return p
	}
	return strings.TrimRight(p, "/") + projectExt
}

// ProjectInfo describes a saved project found in OutputDir.
type ProjectInfo struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Modified time.Time `json:"modified"`
}

// ListProjects returns the .scadproj directories in OutputDir, newest first.
func (s *Service) ListProjects() []ProjectInfo {
	out := []ProjectInfo{}
	entries, err := os.ReadDir(OutputDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), projectExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ProjectInfo{
			Name:     e.Name()[:len(e.Name())-len(projectExt)],
			Path:     filepath.Join(OutputDir, e.Name()),
			Modified: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// savedProject resolves a path from the project list to a saved project directly under
// OutputDir; nothing outside the list can be renamed or deleted through it.
func (s *Service) savedProject(p string) (string, error) {
	dir := filepath.Clean(strings.TrimSpace(p))
	if filepath.Dir(dir) != OutputDir || !strings.HasSuffix(strings.ToLower(dir), projectExt) {
		return "", fmt.Errorf("%q is not a saved project", p)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("project %q not found", p)
	}
	if s.path != "" && samePath(s.path, dir) {
		return "", fmt.Errorf("this project is open; open another one first")
	}
	return dir, nil
}

// DeleteProject removes a saved project from disk.
func (s *Service) DeleteProject(p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	dir, err := s.savedProject(p)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// RenameProject renames a saved project's directory and the name in its project.json.
func (s *Service) RenameProject(p, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return "", err
	}
	dir, err := s.savedProject(p)
	if err != nil {
		return "", err
	}
	name = strings.TrimSuffix(strings.TrimSpace(name), projectExt)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("give a plain project name (no slashes)")
	}
	to := filepath.Join(OutputDir, name+projectExt)
	if _, err := os.Stat(to); err == nil {
		return "", fmt.Errorf("a project named %q already exists", name)
	}
	meta := filepath.Join(dir, "project.json")
	var m map[string]any
	b, err := os.ReadFile(meta)
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil {
		return "", fmt.Errorf("read project.json: %w", err)
	}
	m["name"] = name
	out, _ := json.MarshalIndent(m, "", "  ") // map keys marshal sorted: the canonical form
	if err := os.WriteFile(meta, append(out, '\n'), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(dir, to); err != nil {
		return "", err
	}
	return to, nil
}

// findProject resolves a typed path to an existing project: as typed, then under
// OutputDir, each with and without the .scadproj extension.
func (s *Service) findProject(p string) string {
	for _, c := range []string{p, withProjectExt(p), resolveProjectPath(p), resolveProjectPath(withProjectExt(p))} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return p
}

// Save writes the project; an empty dir saves in place.
func (s *Service) Save(dir string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return "", err
	}
	if dir == "" {
		dir = s.path
	}
	if dir == "" {
		return "", fmt.Errorf("this project has no location yet; give a path")
	}
	dir = resolveProjectPath(withProjectExt(dir))
	if s.path != "" {
		if !samePath(s.path, dir) {
			// "Save as" produces a second project, not a second copy of the same one: two
			// directories claiming one id would make component references ambiguous.
			s.app.ReIdentify()
		}
	}
	// A project keeps the name it was created with ("untitled" by default), so adopt the
	// directory's name on a first save or a "save as": the header should match the file.
	if base := strings.TrimSuffix(filepath.Base(dir), projectExt); base != "" {
		if s.path == "" || !samePath(s.path, dir) || s.app.Meta.Name == "untitled" {
			s.app.Meta.Name = base
		}
	}
	if err := s.app.Save(dir); err != nil {
		return "", err
	}
	s.path, s.dirty = dir, false
	s.hub.Publish(Notice{Kind: "state"})
	return dir, nil
}

// samePath reports whether two project paths point at the same directory.
func samePath(a, b string) bool {
	x, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	y, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return x == y
}

// Export writes STEP or STL through the export tools.
func (s *Service) Export(format, path string) (Outcome, error) {
	if format != "step" && format != "stl" {
		return Outcome{}, fmt.Errorf("format must be step or stl")
	}
	if strings.TrimSpace(path) == "" {
		return Outcome{}, fmt.Errorf("give an output path")
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return Outcome{}, err
	}
	o := s.app.ApplyTool("export_"+format, args, app.PolicyAuto)
	out := Outcome{OK: o.OK, Error: o.Error, Data: o.Data}
	return out, nil
}
