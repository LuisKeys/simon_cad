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
	o := s.app.ApplyTool("export_"+format, args, app.PolicyAuto)
	out := Outcome{OK: o.OK, Error: o.Error, Data: o.Data}
	return out, nil
}
