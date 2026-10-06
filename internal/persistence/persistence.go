// Package persistence reads and writes the .scadproj directory: canonical, byte-stable,
// diff-friendly JSON. Saving the same state twice produces identical bytes.
package persistence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/history"
)

// SchemaVersion is the current on-disk format version. v2 adds assets/images/ (source
// images, only when explicitly retained) and interpretations in agent-history.json; v1
// projects load unchanged.
const SchemaVersion = 2

type Meta struct {
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"name"`
	Units         string `json:"units"`
	OCCTVersion   string `json:"occt_version"`
	Created       string `json:"created,omitempty"`
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
}

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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	}
	for _, f := range files {
		b, err := canonical(f.val)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		if err := writeAtomic(filepath.Join(dir, f.name), b); err != nil {
			return err
		}
	}
	var evs []byte
	for _, e := range p.Events {
		line, _ := json.Marshal(e)
		evs = append(append(evs, line...), '\n')
	}
	if err := writeAtomic(filepath.Join(dir, "events.jsonl"), evs); err != nil {
		return err
	}
	if err := saveImages(dir, p.Images); err != nil {
		return err
	}
	keep := filepath.Join(dir, "thumbnails", ".gitkeep")
	if _, err := os.Stat(keep); err != nil {
		return os.WriteFile(keep, nil, 0o644)
	}
	return nil
}

const imagesDir = "assets/images"

// saveImages writes the retained images and removes ones that are no longer retained. The
// directory only exists when there is something to keep.
func saveImages(dir string, imgs []Image) error {
	full := filepath.Join(dir, filepath.FromSlash(imagesDir))
	want := map[string]bool{}
	for _, im := range imgs {
		want[im.ID+im.Ext] = true
	}
	if entries, err := os.ReadDir(full); err == nil {
		for _, e := range entries {
			if !e.IsDir() && !want[e.Name()] {
				if err := os.Remove(filepath.Join(full, e.Name())); err != nil {
					return err
				}
			}
		}
	}
	if len(imgs) == 0 {
		return nil
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		return err
	}
	for _, im := range imgs {
		if err := writeAtomic(filepath.Join(full, im.ID+im.Ext), im.Data); err != nil {
			return err
		}
	}
	return nil
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
	var err error
	if p.Images, err = loadImages(dir); err != nil {
		return nil, err
	}
	return p, nil
}

// migrate upgrades files from older schema versions. v1 → v2 only added optional content
// (assets/images/, interpretations), so a v1 project is valid as it is; the next save writes v2.
func migrate(dir string, p *Project) error {
	switch p.Meta.SchemaVersion {
	case SchemaVersion, 1:
		return nil
	case 0:
		return fmt.Errorf("project.json has no schema_version")
	}
	return fmt.Errorf("no migration from schema v%d", p.Meta.SchemaVersion)
}
