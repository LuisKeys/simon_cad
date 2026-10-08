// Package components makes projects composable. A component is simply a project with
// reusable set; an instance in another project references it by stable id instead of copying
// its geometry. The package has three separate jobs, deliberately kept apart from rendering
// and UI: Library (what components exist and where), Resolver (instance → geometry) and
// DependencyGraph (what depends on what, and whether that is a cycle).
package components

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/persistence"
)

// ProjectExt is the project directory suffix.
const ProjectExt = ".scadproj"

// DefaultDirName is the conventional projects folder searched next to the open project.
const DefaultDirName = "projects_output"

// Component is one entry of the library: the reusable project's identity and the metadata the
// UI and the agent browse by. It is derived from the project's project.json, never stored twice.
type Component struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	ProjectID   string   `json:"project_id"`
	Path        string   `json:"path"`
	Thumbnail   string   `json:"thumbnail,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Version     int      `json:"version"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// Library finds reusable projects under a set of search roots. Roots are anchored on the open
// project and on configuration, never on the process working directory: resolving a component
// by where the binary happens to run would make the same .scadproj rebuild to different
// geometry depending on the shell, breaking the determinism contract.
type Library struct {
	mu     sync.Mutex
	roots  []string
	cache  map[string]*cachedMeta  // project dir -> last read project.json
	graphs map[string]*cachedGraph // project dir -> last read model.json
}

type cachedMeta struct {
	meta    persistence.Meta
	modTime int64
	size    int64
}

type cachedGraph struct {
	meta    persistence.Meta
	graph   *cadir.Graph
	hash    string
	modTime int64
	size    int64
}

func NewLibrary() *Library {
	return &Library{cache: map[string]*cachedMeta{}, graphs: map[string]*cachedGraph{}}
}

// SetRoots replaces the search roots, in priority order. Missing directories are ignored.
func (l *Library) SetRoots(roots ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roots = nil
	seen := map[string]bool{}
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		l.roots = append(l.roots, abs)
	}
	l.cache, l.graphs = map[string]*cachedMeta{}, map[string]*cachedGraph{}
}

// RootsFor is the standard search order for a project opened at dir: the project's own parent
// directory (sibling components), dir/projects_output, the parent's projects_output, and the
// configured library root last.
func RootsFor(projectDir, configuredRoot string) []string {
	var out []string
	if projectDir != "" {
		parent := filepath.Dir(projectDir)
		out = append(out, parent, filepath.Join(projectDir, DefaultDirName), filepath.Join(parent, DefaultDirName))
	}
	if configuredRoot != "" {
		out = append(out, configuredRoot, filepath.Join(configuredRoot, DefaultDirName))
	}
	return out
}

// Roots returns the current search roots.
func (l *Library) Roots() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.roots...)
}

// scan walks the roots and returns every project directory with its metadata, keyed by project
// id. The first root wins on a duplicate id, which makes a project's own neighbourhood take
// precedence over the shared library.
func (l *Library) scan() map[string]*Component {
	l.mu.Lock()
	roots := append([]string(nil), l.roots...)
	l.mu.Unlock()

	out := map[string]*Component{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() && strings.HasSuffix(e.Name(), ProjectExt) {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names) // deterministic order, so duplicate ids resolve the same way every time
		for _, name := range names {
			dir := filepath.Join(root, name)
			meta, ok := l.meta(dir)
			if !ok || meta.ID == "" {
				continue
			}
			if _, dup := out[meta.ID]; dup {
				continue
			}
			out[meta.ID] = componentOf(meta, dir)
		}
	}
	return out
}

// meta reads a project.json, cached on (mtime, size) so a library scan stays cheap enough to
// run on every rebuild. Only project.json is read: model.json and the agent history are not
// touched until a component is actually resolved.
func (l *Library) meta(dir string) (persistence.Meta, bool) {
	path := filepath.Join(dir, "project.json")
	st, err := os.Stat(path)
	if err != nil {
		return persistence.Meta{}, false
	}
	l.mu.Lock()
	if c, ok := l.cache[dir]; ok && c.modTime == st.ModTime().UnixNano() && c.size == st.Size() {
		l.mu.Unlock()
		return c.meta, true
	}
	l.mu.Unlock()

	b, err := os.ReadFile(path)
	if err != nil {
		return persistence.Meta{}, false
	}
	var m persistence.Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return persistence.Meta{}, false
	}
	if m.ID == "" { // a project that predates schema v3 still has a stable derived id
		m.ID = persistence.DeriveID(m.Name, m.Created)
	}
	l.mu.Lock()
	l.cache[dir] = &cachedMeta{meta: m, modTime: st.ModTime().UnixNano(), size: st.Size()}
	l.mu.Unlock()
	return m, true
}

// LoadGraph reads a project's metadata and CAD-IR, cached on model.json's (mtime, size).
// Resolving an instance happens several times per tool call, so this must not re-read the
// project from disk every time. The returned graph is shared: callers must not mutate it.
func (l *Library) LoadGraph(dir string) (persistence.Meta, *cadir.Graph, string, error) {
	st, err := os.Stat(filepath.Join(dir, "model.json"))
	if err != nil {
		return persistence.Meta{}, nil, "", fmt.Errorf("not a Simon CAD project (%s): %w", dir, err)
	}
	l.mu.Lock()
	if c, ok := l.graphs[dir]; ok && c.modTime == st.ModTime().UnixNano() && c.size == st.Size() {
		l.mu.Unlock()
		return c.meta, c.graph, c.hash, nil
	}
	l.mu.Unlock()

	m, g, err := persistence.LoadGraph(dir)
	if err != nil {
		return m, nil, "", err
	}
	h := g.GeometryHash()
	l.mu.Lock()
	l.graphs[dir] = &cachedGraph{meta: m, graph: g, hash: h, modTime: st.ModTime().UnixNano(), size: st.Size()}
	l.mu.Unlock()
	return m, g, h, nil
}

func componentOf(m persistence.Meta, dir string) *Component {
	c := &Component{ID: m.ID, Name: m.Name, Description: m.Description, ProjectID: m.ID,
		Path: dir, Tags: m.Tags, Version: m.Version, CreatedAt: m.Created}
	if st, err := os.Stat(filepath.Join(dir, "model.json")); err == nil {
		c.UpdatedAt = st.ModTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	if _, err := os.Stat(filepath.Join(dir, "thumbnails", "preview.png")); err == nil {
		c.Thumbnail = filepath.Join("thumbnails", "preview.png")
	}
	return c
}

// List returns the reusable projects, by name.
func (l *Library) List() []*Component {
	var out []*Component
	for _, c := range l.scan() {
		if m, ok := l.meta(c.Path); ok && m.Reusable {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Search filters List by a case-insensitive substring of name, description or tags.
func (l *Library) Search(q string) []*Component {
	q = strings.ToLower(strings.TrimSpace(q))
	all := l.List()
	if q == "" {
		return all
	}
	var out []*Component
	for _, c := range all {
		hay := strings.ToLower(c.Name + " " + c.Description + " " + strings.Join(c.Tags, " "))
		if strings.Contains(hay, q) {
			out = append(out, c)
		}
	}
	return out
}

// Find resolves a project id to its library entry. It accepts a component's name as a
// fallback, so an agent that says "Wheel" still lands on the right project when the name is
// unambiguous.
func (l *Library) Find(idOrName string) (*Component, error) {
	if idOrName == "" {
		return nil, fmt.Errorf("no component given")
	}
	byID := l.scan()
	if c, ok := byID[idOrName]; ok {
		return c, nil
	}
	var matches []*Component
	for _, c := range l.List() {
		if strings.EqualFold(c.Name, idOrName) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no component %q in the library (roots: %s)", idOrName, strings.Join(l.Roots(), ", "))
	}
	ids := make([]string, len(matches))
	for i, m := range matches {
		ids[i] = m.ID
	}
	return nil, fmt.Errorf("%q is ambiguous, %d components share that name (%s)", idOrName, len(matches), strings.Join(ids, ", "))
}

// Path resolves a project id to its directory, whether or not it is marked reusable. A
// non-reusable project is still resolvable so that an existing instance keeps working if the
// user un-publishes its component.
func (l *Library) Path(projectID string) (string, error) {
	if c, ok := l.scan()[projectID]; ok {
		return c.Path, nil
	}
	return "", fmt.Errorf("component project %q not found (roots: %s)", projectID, strings.Join(l.Roots(), ", "))
}

// ---- derived project composition --------------------------------------------------------

// Dependency is one reusable project a project depends on, with the instances that reference it.
type Dependency struct {
	ProjectID   string   `json:"project_id"`
	Name        string   `json:"name,omitempty"`
	Version     string   `json:"version,omitempty"`
	ContentHash string   `json:"content_hash,omitempty"`
	Instances   []string `json:"instances"`
}

// Dependencies derives a project's component dependencies from its graph. Nothing is
// maintained alongside the instances: the instances are the single source of truth.
func Dependencies(g *cadir.Graph) []Dependency {
	byID := map[string]*Dependency{}
	var order []string
	for _, n := range g.Nodes {
		if n.Type != features.TypeComponentInstance {
			continue
		}
		id, _ := n.Parameters[features.ParamComponent].(string)
		if id == "" {
			continue
		}
		d, ok := byID[id]
		if !ok {
			version, _ := n.Parameters[features.ParamVersion].(string)
			hash, _ := n.Parameters[features.ParamContentHash].(string)
			d = &Dependency{ProjectID: id, Name: n.Metadata.Name, Version: version, ContentHash: hash}
			byID[id] = d
			order = append(order, id)
		}
		d.Instances = append(d.Instances, n.ID)
	}
	sort.Strings(order)
	out := make([]Dependency, 0, len(order))
	for _, id := range order {
		d := byID[id]
		sort.Strings(d.Instances)
		out = append(out, *d)
	}
	return out
}

// Manifest is the canonical components.json body: the derived dependency list. It is written
// on save and never read back to rebuild, so it can never disagree with the graph.
func Manifest(g *cadir.Graph) json.RawMessage {
	deps := Dependencies(g)
	if deps == nil {
		deps = []Dependency{}
	}
	b, err := json.Marshal(struct {
		Components []Dependency `json:"components"`
	}{deps})
	if err != nil {
		return nil
	}
	return b
}
