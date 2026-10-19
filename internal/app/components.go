package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/components"
	"github.com/accelone/simoncad/internal/events"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/persistence"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// PublishComponent writes nodes out as a new reusable project and returns its stable id.
//
// This is the one part of "create component" that is not a ChangeSet: writing a sibling
// project to disk cannot be rolled back by the command layer. The directory name is therefore
// content-addressed — the same nodes always publish to the same directory — so undo followed
// by redo lands on that directory again instead of scattering copies. Undoing without redoing
// leaves the library project behind; that is deliberate and is what the UI reports.
func (a *App) PublishComponent(name, description string, tags []string, nodes []*cadir.Node) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("a component needs a name")
	}
	if len(nodes) == 0 {
		return "", fmt.Errorf("a component needs at least one feature")
	}
	g := &cadir.Graph{Units: a.Graph.Units, Nodes: nodes}
	if err := g.Validate(); err != nil {
		return "", fmt.Errorf("the selected features do not form a valid component: %w", err)
	}
	root, err := a.libraryRoot()
	if err != nil {
		return "", err
	}
	// The id and the directory are derived from the content, so republishing the same
	// selection is idempotent rather than duplicating the component.
	sum := sha256.Sum256([]byte(name + "\x00" + g.GeometryHash()))
	stamp := hex.EncodeToString(sum[:4])
	id := "prj_" + hex.EncodeToString(sum[:6])
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if slug == "" {
		slug = "component"
	}
	dir := filepath.Join(root, fmt.Sprintf("%s_%s%s", slug, stamp, components.ProjectExt))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	now := a.Clock().UTC().Format(time.RFC3339)
	p := &persistence.Project{
		Meta: persistence.Meta{SchemaVersion: persistence.SchemaVersion, ID: id, Name: name,
			Units: g.Units, OCCTVersion: a.K.Version(), Created: now,
			Reusable: true, Description: description, Tags: tags, Version: 1, DerivedFrom: a.Meta.ID},
		Graph: g,
		History: persistence.AgentHistory{Prompts: []persistence.Prompt{}, ToolCalls: []persistence.ToolCall{},
			ChangeSets: []*commands.ChangeSet{}, Checkpoints: []*history.Checkpoint{}},
		Components: components.Manifest(g),
		Meshes:     a.meshesFor(nodes),
		Steps:      a.stepsFor(nodes),
		Workspace:  &a.Workspace, // a component opens with the units it was made in
	}
	if err := persistence.Save(dir, p); err != nil {
		return "", err
	}
	a.Lib.SetRoots(components.RootsFor(a.dir, configComponentLibrary())...) // drop cached scans
	a.Bus.Publish(events.ComponentCreated, map[string]any{"project_id": id, "name": name, "path": dir,
		"features": idsOf(nodes)})
	return id, nil
}

// libraryRoot is where a newly created component is written: the configured library root when
// there is one, else the project's own neighbourhood. An unsaved project has no neighbourhood,
// so it must be saved (or a library configured) before it can publish a component.
func (a *App) libraryRoot() (string, error) {
	if r := configComponentLibrary(); r != "" {
		return r, nil
	}
	if a.dir == "" {
		return "", fmt.Errorf("save the project first, or set a component library folder in Settings: " +
			"a component is written next to the project it came from")
	}
	return filepath.Dir(a.dir), nil
}

func idsOf(nodes []*cadir.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

// ---- library operations -------------------------------------------------------------------
//
// These change the library, not the model: they move no geometry and create no ChangeSet, so
// they are App/service operations rather than tools. Editing a component's *geometry* is
// opening its project and using the ordinary tools on it.

// ListComponents returns the library entries, optionally filtered by a search string.
func (a *App) ListComponents(query string) []*components.Component {
	if strings.TrimSpace(query) == "" {
		return a.Lib.List()
	}
	return a.Lib.Search(query)
}

// ComponentPath is the directory of a component's project.
func (a *App) ComponentPath(projectID string) (string, error) { return a.Lib.Path(projectID) }

// ComponentsOutOfDate maps each instance in this project whose component has changed on disk
// to the content hash it would move to. The UI marks those rows and offers to sync.
func (a *App) ComponentsOutOfDate() map[string]string { return a.Resolver.OutOfDate(a.Graph) }

// RenameComponent updates a component's library metadata in its own project.json.
func (a *App) RenameComponent(projectID, name, description string, tags []string) error {
	dir, err := a.Lib.Path(projectID)
	if err != nil {
		return err
	}
	if dir == a.dir {
		return fmt.Errorf("this project is the component %q; rename it by saving it under a new name", name)
	}
	p, err := persistence.Load(dir)
	if err != nil {
		return err
	}
	if n := strings.TrimSpace(name); n != "" {
		p.Meta.Name = n
	}
	p.Meta.Description, p.Meta.Tags, p.Meta.Reusable = description, tags, true
	if err := persistence.Save(dir, p); err != nil {
		return err
	}
	a.Lib.SetRoots(components.RootsFor(a.dir, configComponentLibrary())...)
	return nil
}

// DeleteComponent removes a component's project from disk. It refuses while the open project
// still references it: a dangling reference would fail to rebuild with no way back.
func (a *App) DeleteComponent(projectID string) error {
	dir, err := a.Lib.Path(projectID)
	if err != nil {
		return err
	}
	if dir == a.dir {
		return fmt.Errorf("cannot delete the project that is currently open")
	}
	var used []string
	for _, d := range components.Dependencies(a.Graph) {
		if d.ProjectID == projectID {
			used = append(used, d.Instances...)
		}
	}
	if len(used) > 0 {
		return fmt.Errorf("this project still uses the component in %s: detach or delete those first",
			strings.Join(used, ", "))
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	a.Lib.SetRoots(components.RootsFor(a.dir, configComponentLibrary())...)
	return nil
}
