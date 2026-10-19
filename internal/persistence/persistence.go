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
	"strings"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/meshimport"
	"github.com/accelone/simoncad/internal/stepimport"
	"github.com/accelone/simoncad/internal/workspace"
)

// SchemaVersion is the current on-disk format version. v2 adds assets/images/ (source
// images, only when explicitly retained) and interpretations in agent-history.json; v3 adds
// the stable project id and the reusable-component metadata; v4 adds workspace.json (units,
// grid and snapping); v5 adds assets/documents/ (attached reference documents) and stores
// every image attached to a prompt; v6 adds assets/meshes/ (validated STL meshes that
// import_mesh nodes reference); v7 adds assets/steps/ (STEP files that import_step nodes
// reference). Every addition is optional, so v1–v6 projects load unchanged.
const SchemaVersion = 7

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
	Documents  []string `json:"documents,omitempty"`     // ids of documents first sent with the prompt
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
	// Documents index the attached reference documents stored under assets/documents/.
	Documents []DocumentInfo `json:"documents,omitempty"`
}

// DocumentInfo describes a stored reference document. Paths are relative to the project.
type DocumentInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	File        string `json:"file"`                   // the original
	Extracted   string `json:"extracted,omitempty"`    // the text a model extracted from it (PDF)
	ExtractedBy string `json:"extracted_by,omitempty"` // that model
	Active      bool   `json:"active,omitempty"`       // still attached to the conversation
}

// Document is a reference document stored under assets/documents/.
type Document struct {
	ID          string // doc_<hash>
	Name        string
	Ext         string // of the original: .pdf | .txt | .md | …
	Data        []byte // the original
	Extracted   string // text extracted from the original ("" for a text document)
	ExtractedBy string
	Active      bool
}

// Mesh is a validated, canonical binary STL stored under assets/meshes/<id>.stl. Its id is the
// hash of its content (meshimport.Prepare), so a file that no longer matches is detected on use.
type Mesh struct {
	ID   string // mesh_<hash>
	Data []byte
}

// Step is a STEP file stored as it came under assets/steps/<id>.step. Its id is the hash of its
// content (stepimport.ID), so a file that no longer matches is detected on use.
type Step struct {
	ID   string // step_<hash>
	Data []byte
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
	Documents   []Document
	// Meshes are the validated meshes the graph's import_mesh nodes reference (assets/meshes/).
	Meshes []Mesh
	// Steps are the STEP files the graph's import_step nodes reference (assets/steps/).
	Steps []Step
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
		{"agent-history.json", historyWithDocuments(p.History, p.Documents)},
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
	docs, err := saveDocuments(dir, p.Documents)
	if err != nil {
		return err
	}
	meshes, err := saveMeshes(dir, p.Meshes)
	if err != nil {
		return err
	}
	steps, err := saveSteps(dir, p.Steps)
	if err != nil {
		return err
	}
	if err := writeAll(append(append(append(append(pending, imgs...), docs...), meshes...), steps...)); err != nil {
		return err
	}
	keep := filepath.Join(dir, "thumbnails", ".gitkeep")
	if _, err := os.Stat(keep); err != nil {
		return os.WriteFile(keep, nil, 0o644)
	}
	return nil
}

const (
	imagesDir    = "assets/images"
	documentsDir = "assets/documents"
	meshesDir    = "assets/meshes"
	stepsDir     = "assets/steps"
)

// MeshPath is where a project stores an imported mesh.
func MeshPath(dir, id string) string {
	return filepath.Join(dir, filepath.FromSlash(meshesDir), id+".stl")
}

// saveMeshes removes stored meshes nothing references any more and returns the files to write.
func saveMeshes(dir string, meshes []Mesh) ([]pendingFile, error) {
	full := filepath.Join(dir, filepath.FromSlash(meshesDir))
	out := make([]pendingFile, 0, len(meshes))
	for _, m := range meshes {
		if !meshimport.ValidID(m.ID) {
			return nil, fmt.Errorf("mesh id %q is not valid", m.ID)
		}
		out = append(out, pendingFile{MeshPath(dir, m.ID), m.Data})
	}
	return out, prune(full, out)
}

func loadMeshes(dir string) ([]Mesh, error) {
	entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(meshesDir)))
	if err != nil {
		return nil, nil // no meshes
	}
	var out []Mesh
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".stl")
		if e.IsDir() || !ok || !meshimport.ValidID(id) {
			continue
		}
		b, err := os.ReadFile(MeshPath(dir, id))
		if err != nil {
			return nil, err
		}
		out = append(out, Mesh{ID: id, Data: b})
	}
	return out, nil
}

// StepPath is where a project stores an imported STEP file.
func StepPath(dir, id string) string {
	return filepath.Join(dir, filepath.FromSlash(stepsDir), id+".step")
}

// saveSteps removes stored STEP files nothing references any more and returns the files to write.
func saveSteps(dir string, steps []Step) ([]pendingFile, error) {
	full := filepath.Join(dir, filepath.FromSlash(stepsDir))
	out := make([]pendingFile, 0, len(steps))
	for _, s := range steps {
		if !stepimport.ValidID(s.ID) {
			return nil, fmt.Errorf("STEP id %q is not valid", s.ID)
		}
		out = append(out, pendingFile{StepPath(dir, s.ID), s.Data})
	}
	return out, prune(full, out)
}

func loadSteps(dir string) ([]Step, error) {
	entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(stepsDir)))
	if err != nil {
		return nil, nil // no STEP files
	}
	var out []Step
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".step")
		if e.IsDir() || !ok || !stepimport.ValidID(id) {
			continue
		}
		b, err := os.ReadFile(StepPath(dir, id))
		if err != nil {
			return nil, err
		}
		out = append(out, Step{ID: id, Data: b})
	}
	return out, nil
}

// StepFiles resolves import_step nodes from the assets/steps/ of one project directory. A
// reusable component is built from its own directory with it.
type StepFiles string

// Step reads the stored file; the caller checks it against the id (stepimport.Load).
func (d StepFiles) Step(id string) ([]byte, error) {
	if !stepimport.ValidID(id) {
		return nil, fmt.Errorf("%q is not a STEP id", id)
	}
	b, err := os.ReadFile(StepPath(string(d), id))
	if err != nil {
		return nil, fmt.Errorf("STEP file %s is missing from the project's assets/steps/", id)
	}
	return b, nil
}

// MeshFiles resolves import_mesh nodes from the assets/meshes/ of one project directory. A
// reusable component is built from its own directory with it.
type MeshFiles string

// Mesh reads and verifies the stored mesh.
func (d MeshFiles) Mesh(id string) (*meshimport.Mesh, error) {
	if !meshimport.ValidID(id) {
		return nil, fmt.Errorf("%q is not a mesh id", id)
	}
	b, err := os.ReadFile(MeshPath(string(d), id))
	if err != nil {
		return nil, fmt.Errorf("mesh %s is missing from the project's assets/meshes/", id)
	}
	return meshimport.Decode(id, b)
}

// ImagePath is where a project stores an image.
func ImagePath(dir, id, ext string) string {
	return filepath.Join(dir, filepath.FromSlash(imagesDir), id+ext)
}

// DocumentPath is where a project stores a document's original.
func DocumentPath(dir, id, ext string) string {
	return filepath.Join(dir, filepath.FromSlash(documentsDir), id+ext)
}

// extractedName is the file holding the text extracted from a document.
func extractedName(id string) string { return id + ".extracted.md" }

// historyWithDocuments indexes the stored documents in agent-history.json.
func historyWithDocuments(h AgentHistory, docs []Document) AgentHistory {
	h.Documents = nil
	for _, d := range docs {
		info := DocumentInfo{ID: d.ID, Name: d.Name, File: documentsDir + "/" + d.ID + d.Ext, ExtractedBy: d.ExtractedBy, Active: d.Active}
		if d.Extracted != "" {
			info.Extracted = documentsDir + "/" + extractedName(d.ID)
		}
		h.Documents = append(h.Documents, info)
	}
	return h
}

// saveDocuments removes stored documents that are gone and returns the files to write for
// the ones that remain (the original, plus the extracted text of a PDF).
func saveDocuments(dir string, docs []Document) ([]pendingFile, error) {
	full := filepath.Join(dir, filepath.FromSlash(documentsDir))
	var out []pendingFile
	for _, d := range docs {
		out = append(out, pendingFile{filepath.Join(full, d.ID+d.Ext), d.Data})
		if d.Extracted != "" {
			out = append(out, pendingFile{filepath.Join(full, extractedName(d.ID)), []byte(d.Extracted)})
		}
	}
	return out, prune(full, out)
}

// prune removes the files of dir that are not about to be written, and creates dir when
// something will be: an asset directory only exists when there is something to keep.
func prune(full string, keep []pendingFile) error {
	want := map[string]bool{}
	for _, f := range keep {
		want[filepath.Base(f.path)] = true
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
	if len(keep) == 0 {
		return nil
	}
	return os.MkdirAll(full, 0o755)
}

// loadDocuments reads the documents agent-history.json indexes. A document whose original
// is missing is skipped: the project still opens, only that attachment is lost.
func loadDocuments(dir string, infos []DocumentInfo) ([]Document, error) {
	var out []Document
	for _, info := range infos {
		if !validAsset(info.File, documentsDir) {
			return nil, fmt.Errorf("agent-history.json: document %s is outside %s/", info.ID, documentsDir)
		}
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(info.File)))
		if err != nil {
			continue
		}
		d := Document{ID: info.ID, Name: info.Name, Ext: filepath.Ext(info.File), Data: b, ExtractedBy: info.ExtractedBy, Active: info.Active}
		if info.Extracted != "" {
			if !validAsset(info.Extracted, documentsDir) {
				return nil, fmt.Errorf("agent-history.json: document %s is outside %s/", info.ID, documentsDir)
			}
			t, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(info.Extracted)))
			if err != nil {
				continue
			}
			d.Extracted = string(t)
		}
		out = append(out, d)
	}
	return out, nil
}

// validAsset reports whether rel names a file directly inside the asset directory sub, so a
// crafted agent-history.json cannot make the project read files from elsewhere.
func validAsset(rel, sub string) bool {
	base := strings.TrimPrefix(rel, sub+"/")
	return base != rel && base != "" && base != "." && base != ".." && !strings.ContainsAny(base, `/\:`)
}

// saveImages removes retained images that are gone and returns the files to write for the
// ones that remain. The directory only exists when there is something to keep.
func saveImages(dir string, imgs []Image) ([]pendingFile, error) {
	full := filepath.Join(dir, filepath.FromSlash(imagesDir))
	out := make([]pendingFile, 0, len(imgs))
	for _, im := range imgs {
		out = append(out, pendingFile{filepath.Join(full, im.ID+im.Ext), im.Data})
	}
	return out, prune(full, out)
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
	if p.Documents, err = loadDocuments(dir, p.History.Documents); err != nil {
		return nil, err
	}
	if p.Meshes, err = loadMeshes(dir); err != nil {
		return nil, err
	}
	if p.Steps, err = loadSteps(dir); err != nil {
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
// metadata; v4: workspace.json; v5: assets/documents/; v6: assets/meshes/; v7: assets/steps/), so an older project is valid as it is; the next save writes
// the current version.
func migrate(dir string, p *Project) error {
	switch p.Meta.SchemaVersion {
	case SchemaVersion, 6, 5, 4, 3, 2, 1:
		if p.Meta.ID == "" {
			p.Meta.ID = DeriveID(p.Meta.Name, p.Meta.Created)
		}
		return nil
	case 0:
		return fmt.Errorf("project.json has no schema_version")
	}
	return fmt.Errorf("no migration from schema v%d", p.Meta.SchemaVersion)
}
