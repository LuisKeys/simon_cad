// Package persistence reads and writes the .scadproj directory: canonical, byte-stable,
// diff-friendly JSON. Saving the same state twice produces identical bytes.
package persistence

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/workspace"
)

// SchemaVersion is the current on-disk format version. v2 adds assets/images/ (source
// images, only when explicitly retained) and interpretations in agent-history.json; v3 adds
// the stable project id and the reusable-component metadata; v4 adds workspace.json (units,
// grid and snapping). Every addition is optional, so v1–v3 projects load unchanged.
const SchemaVersion = 4

// Meta is project.json. ID is the stable identity a ComponentInstance references: it survives
// renaming the directory, and the directory name never does. A project with Reusable set is a
// component — there is no separate "component" concept.
type Meta struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id,omitempty"` // prj_<12 hex>
	Name          string `json:"name"`
	Units         string `json:"units"`
	OCCTVersion   string `json:"occt_version"`
	Created       string `json:"created,omitempty"`
	// Reusable marks this project as a component: it appears in the component library and may
	// be instantiated by other projects.
	Reusable    bool     `json:"reusable,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Version counts saves of a reusable project. It is informational in v1 (instances pin a
	// content hash, not a version) and is the hook for explicit version locking later.
	Version int `json:"component_version,omitempty"`
	// DerivedFrom records the project id this one was copied from by "save as".
	DerivedFrom string `json:"derived_from,omitempty"`
}

// NewID mints a fresh stable project id.
func NewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return "prj_" + hex.EncodeToString(b[:])
}

// DeriveID is the stable id of a project that predates schema v3: the same legacy project
// always migrates to the same id, so re-migrating never churns its bytes.
func DeriveID(name, created string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + created))
	return "prj_" + hex.EncodeToString(sum[:6])
}

type Prompt struct {
	ID         string   `json:"id"`
	Time       string   `json:"time"`
	Text       string   `json:"text"`
	Selection  []string `json:"selection,omitempty"`
	Capability string   `json:"capability,omitempty"`
	Model      string   `json:"model,omitempty"`
	Summary    string   `json:"agent_summary,omitempty"` // the agent's final explanation
	Images     []string `json:"images,omitempty"`        // ids of images attached to the prompt
}

type ToolCall struct {
	ID          string          `json:"id"`
	PromptID    string          `json:"prompt_id,omitempty"`
	Actor       string          `json:"actor"`
	Name        string          `json:"name"`
	Args        json.RawMessage `json:"args"`
	Status      string          `json:"status"` // ok|rejected|invalid
	Error       string          `json:"error,omitempty"`
	ChangeSetID string          `json:"changeset_id,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

// AgentHistory is agent-history.json: the complete intent → tool call → ChangeSet →
// checkpoint trace.
type AgentHistory struct {
	Prompts     []Prompt              `json:"prompts"`
	ToolCalls   []ToolCall            `json:"tool_calls"`
	ChangeSets  []*commands.ChangeSet `json:"changesets"`
	Checkpoints []*history.Checkpoint `json:"checkpoints"`
	// Interpretations are the image proposals (vision capability). They explain image-derived
	// features but are never needed to rebuild geometry.
	Interpretations json.RawMessage `json:"interpretations,omitempty"`
	// Visual is the visual-refinement state: references and their analysis, regions and
	// locks, the iteration history (scores, changes, parameters). Renders are not saved.
	Visual json.RawMessage `json:"visual,omitempty"`
}

// Image is a retained source image stored under assets/images/.
type Image struct {
	ID   string // img_<hash>
	Ext  string // .png | .jpg | .webp
	Data []byte
}

type Project struct {
	Meta        Meta
	Graph       *cadir.Graph
	History     AgentHistory
	Events      []events.Event
	Components  json.RawMessage
	Constraints json.RawMessage
	Materials   json.RawMessage
	Images      []Image
	// Workspace is the project's units, grid and snapping (workspace.json). Load leaves it
	// nil for a project saved before it existed; Save writes the factory defaults for nil.
	Workspace *workspace.Settings
	// WorkspaceWarnings say which parts of workspace.json could not be read and were
	// replaced by defaults (unknown fields are ignored silently).
	WorkspaceWarnings []string
}

// WorkspaceFile holds a project's workspace settings.
const WorkspaceFile = "workspace.json"

var (
	emptyComponents  = json.RawMessage(`{"components": []}`)
	emptyConstraints = json.RawMessage(`{"constraints": []}`)
	emptyMaterials   = json.RawMessage(`{"materials": []}`)
)

func canonical(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func writeAtomic(path string, data []byte) error {
	return writeAll([]pendingFile{{path, data}})
}

// pendingFile is one file a save wants on disk.
type pendingFile struct {
	path string
	data []byte
}

// unchanged reports whether path already holds exactly data.
func unchanged(path string, data []byte) bool {
	cur, err := os.ReadFile(path)
	return err == nil && bytes.Equal(cur, data)
}

// writeSynced writes data to path and flushes it to disk, so a rename of it can never
// expose a file whose contents were lost in a crash.
func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeAll puts files on disk in two phases. Phase one stages each changed file next to its
// destination (written and synced); only if every one of them was staged does phase two
// rename them into place. A failure while staging (full disk, permissions) therefore leaves
// the previous project completely intact instead of a mix of old and new files, and the
// window in which a crash can leave a mix is reduced to the renames. Files whose content
// already matches are not touched at all: no I/O, no new modification time.
func writeAll(files []pendingFile) error {
	var staged []pendingFile
	discard := func() {
		for _, f := range staged {
			os.Remove(f.path + ".tmp")
		}
	}
	for _, f := range files {
		if unchanged(f.path, f.data) {
			continue
		}
		if err := writeSynced(f.path+".tmp", f.data); err != nil {
			os.Remove(f.path + ".tmp")
			discard()
			return err
		}
		staged = append(staged, f)
	}
	for i, f := range staged {
		if err := os.Rename(f.path+".tmp", f.path); err != nil {
			staged = staged[i:] // the ones before it are in place; drop the rest
			discard()
			return err
		}
	}
	return nil
}

// Save writes the project directory.
func Save(dir string, p *Project) error {
	if err := os.MkdirAll(filepath.Join(dir, "thumbnails"), 0o755); err != nil {
		return err
	}
	p.Meta.SchemaVersion = SchemaVersion
	if p.Meta.Units == "" {
		p.Meta.Units = "mm"
	}
	raw := func(r json.RawMessage, def json.RawMessage) any {
		if len(r) == 0 {
			return def
		}
		return r
	}
	ws := workspace.Defaults()
	if p.Workspace != nil {
		ws = p.Workspace.Normalized()
	}
	files := []struct {
		name string
		val  any
	}{
		{"project.json", p.Meta},
		{"model.json", p.Graph},
		{"components.json", raw(p.Components, emptyComponents)},
		{"constraints.json", raw(p.Constraints, emptyConstraints)},
		{"materials.json", raw(p.Materials, emptyMaterials)},
		{"agent-history.json", p.History},
		{WorkspaceFile, ws},
	}
	var pending []pendingFile
	for _, f := range files {
		b, err := canonical(f.val)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		pending = append(pending, pendingFile{filepath.Join(dir, f.name), b})
	}
	var evs []byte
	for _, e := range p.Events {
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("events: %w", err)
		}
		evs = append(append(evs, line...), '\n')
	}
	pending = append(pending, pendingFile{filepath.Join(dir, "events.jsonl"), evs})
	imgs, err := saveImages(dir, p.Images)
	if err != nil {
		return err
	}
	if err := writeAll(append(pending, imgs...)); err != nil {
		return err
	}
	keep := filepath.Join(dir, "thumbnails", ".gitkeep")
	if _, err := os.Stat(keep); err != nil {
		return os.WriteFile(keep, nil, 0o644)
	}
	return nil
}

const imagesDir = "assets/images"

// saveImages removes retained images that are gone and returns the files to write for the
// ones that remain. The directory only exists when there is something to keep.
func saveImages(dir string, imgs []Image) ([]pendingFile, error) {
	full := filepath.Join(dir, filepath.FromSlash(imagesDir))
	want := map[string]bool{}
	for _, im := range imgs {
		want[im.ID+im.Ext] = true
	}
	if entries, err := os.ReadDir(full); err == nil {
		for _, e := range entries {
			if !e.IsDir() && !want[e.Name()] {
				if err := os.Remove(filepath.Join(full, e.Name())); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(imgs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		return nil, err
	}
	out := make([]pendingFile, 0, len(imgs))
	for _, im := range imgs {
		out = append(out, pendingFile{filepath.Join(full, im.ID+im.Ext), im.Data})
	}
	return out, nil
}

func loadImages(dir string) ([]Image, error) {
	full := filepath.Join(dir, filepath.FromSlash(imagesDir))
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, nil // no retained images
	}
	var out []Image
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".png" && ext != ".jpg" && ext != ".webp") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(full, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Image{ID: e.Name()[:len(e.Name())-len(ext)], Ext: ext, Data: b})
	}
	return out, nil
}

func readJSON(dir, name string, v any) error {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Load reads a project directory, migrating older schema versions in memory.
func Load(dir string) (*Project, error) {
	p := &Project{Graph: cadir.NewGraph()}
	if err := readJSON(dir, "project.json", &p.Meta); err != nil {
		return nil, fmt.Errorf("not a Simon CAD project (%s): %w", dir, err)
	}
	if p.Meta.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("project schema v%d is newer than this build supports (v%d)", p.Meta.SchemaVersion, SchemaVersion)
	}
	if err := migrate(dir, p); err != nil {
		return nil, err
	}
	if err := readJSON(dir, "model.json", p.Graph); err != nil {
		return nil, err
	}
	if p.Graph.Nodes == nil {
		p.Graph.Nodes = []*cadir.Node{}
	}
	if err := p.Graph.Validate(); err != nil {
		return nil, fmt.Errorf("model.json: %w", err)
	}
	if err := readJSON(dir, "agent-history.json", &p.History); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		name string
		dst  *json.RawMessage
	}{{"components.json", &p.Components}, {"constraints.json", &p.Constraints}, {"materials.json", &p.Materials}} {
		if b, err := os.ReadFile(filepath.Join(dir, f.name)); err == nil {
			*f.dst = b
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "events.jsonl")); err == nil {
		if p.Events, err = events.ParseJSONL(b); err != nil {
			return nil, err
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, WorkspaceFile)); err == nil {
		ws, warns, err := workspace.Decode(b, workspace.Defaults())
		if err != nil {
			warns = []string{err.Error() + "; defaults used"}
		}
		p.Workspace, p.WorkspaceWarnings = &ws, warns
	}
	var err error
	if p.Images, err = loadImages(dir); err != nil {
		return nil, err
	}
	return p, nil
}

// LoadGraph reads only project.json and model.json. Resolving a component instance runs
// several times per tool call (Propose evaluates twice, the commit refresh once more), so it
// must never touch agent-history.json, which can be megabytes of trace.
func LoadGraph(dir string) (Meta, *cadir.Graph, error) {
	var m Meta
	if err := readJSON(dir, "project.json", &m); err != nil {
		return m, nil, fmt.Errorf("not a Simon CAD project (%s): %w", dir, err)
	}
	if m.SchemaVersion > SchemaVersion {
		return m, nil, fmt.Errorf("project schema v%d is newer than this build supports (v%d)", m.SchemaVersion, SchemaVersion)
	}
	if m.ID == "" {
		m.ID = DeriveID(m.Name, m.Created)
	}
	g := cadir.NewGraph()
	if err := readJSON(dir, "model.json", g); err != nil {
		return m, nil, err
	}
	if g.Nodes == nil {
		g.Nodes = []*cadir.Node{}
	}
	if err := g.Validate(); err != nil {
		return m, nil, fmt.Errorf("model.json: %w", err)
	}
	return m, g, nil
}

// migrate upgrades files from older schema versions. Every version bump so far only added
// optional content (v2: assets/images/, interpretations; v3: the project id and component
// metadata; v4: workspace.json), so an older project is valid as it is; the next save writes
// the current version.
func migrate(dir string, p *Project) error {
	switch p.Meta.SchemaVersion {
	case SchemaVersion, 3, 2, 1:
		if p.Meta.ID == "" {
			p.Meta.ID = DeriveID(p.Meta.Name, p.Meta.Created)
		}
		return nil
	case 0:
		return fmt.Errorf("project.json has no schema_version")
	}
	return fmt.Errorf("no migration from schema v%d", p.Meta.SchemaVersion)
}
