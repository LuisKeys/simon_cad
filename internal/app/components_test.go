package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/geometry/features"
)

// libraryAt points the component library at dir for the duration of the test, so a test never
// reads or writes the user's configured library.
func libraryAt(t *testing.T, dir string) {
	t.Helper()
	prev := configComponentLibrary
	configComponentLibrary = func() string { return dir }
	t.Cleanup(func() { configComponentLibrary = prev })
}

// newInLibrary starts a project whose library root is dir.
func newInLibrary(t *testing.T, name, dir string) *App {
	t.Helper()
	libraryAt(t, dir)
	a := New(name, clock)
	a.useLibraryRoots("")
	return a
}

func instanceCount(a *App) int {
	n := 0
	for _, node := range a.Graph.Nodes {
		if node.Type == features.TypeComponentInstance {
			n++
		}
	}
	return n
}

// A component is a reusable project; creating one moves geometry out and leaves a reference
// behind, and the resolved instance has exactly the volume the geometry had.
func TestCreateComponentReplacesGeometryWithAReference(t *testing.T) {
	lib := t.TempDir()
	a := newInLibrary(t, "car", lib)
	defer a.Close()

	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"wheel","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	before := vol(t, a)

	mustOK(t, a.ApplyTool("create_component",
		[]byte(`{"name":"Wheel","description":"a road wheel","tags":["chassis"],"features":["wheel"]}`), PolicyCommit))

	if a.Graph.Get("wheel") != nil {
		t.Fatal("the original geometry should have moved into the component")
	}
	if instanceCount(a) != 1 {
		t.Fatalf("want one component instance, got %d", instanceCount(a))
	}
	if got := vol(t, a); !near(got, before) {
		t.Fatalf("the instance should resolve to the same volume: got %g want %g", got, before)
	}

	lb := a.Lib.List()
	if len(lb) != 1 || lb[0].Name != "Wheel" || lb[0].Description != "a road wheel" {
		t.Fatalf("the component should appear in the library: %+v", lb)
	}
	// Nothing of the component's geometry is duplicated into the parent.
	for _, n := range a.Graph.Nodes {
		if n.Type == "box" {
			t.Fatal("the parent still holds a copy of the component's geometry")
		}
	}
}

// Four instances of one component are four references, and the component's geometry is stored once.
func TestInsertComponentPlacesIndependentInstances(t *testing.T) {
	lib := t.TempDir()
	a := newInLibrary(t, "car", lib)
	defer a.Close()

	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"wheel","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	one := vol(t, a)
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Wheel","features":["wheel"]}`), PolicyCommit))

	for _, at := range []string{`{"component":"Wheel","name":"Wheel FR","dx":100}`,
		`{"component":"Wheel","name":"Wheel RL","dy":100}`,
		`{"component":"Wheel","name":"Wheel RR","dx":100,"dy":100}`} {
		mustOK(t, a.ApplyTool("insert_component", []byte(at), PolicyCommit))
	}
	if instanceCount(a) != 4 {
		t.Fatalf("want four instances, got %d", instanceCount(a))
	}
	if got := vol(t, a); !near(got, 4*one) {
		t.Fatalf("four instances should be four bodies: got %g want %g", got, 4*one)
	}
	// Each instance is one leaf body, so the design tree and the viewport see four objects.
	m, err := a.Model()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Leaves) != 4 {
		t.Fatalf("want four leaf bodies, got %d", len(m.Leaves))
	}
}

// Editing a component is editing its project. The parent notices, and syncing is an ordinary,
// undoable ChangeSet.
func TestEditingAComponentPropagatesOnSync(t *testing.T) {
	lib := t.TempDir()
	parentDir := filepath.Join(lib, "car.scadproj")
	a := newInLibrary(t, "car", lib)

	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"wheel","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Wheel","features":["wheel"]}`), PolicyCommit))
	mustOK(t, a.ApplyTool("insert_component", []byte(`{"component":"Wheel","dx":50}`), PolicyCommit))
	before := vol(t, a)
	if err := a.Save(parentDir); err != nil {
		t.Fatal(err)
	}
	compPath := a.Lib.List()[0].Path
	a.Close()

	// Open the component as a normal project and make it bigger.
	c, err := Open(compPath, clock)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, c.ApplyTool("modify_parameter", []byte(`{"feature":"wheel","parameter":"dz","value":20}`), PolicyCommit))
	if err := c.Save(compPath); err != nil {
		t.Fatal(err)
	}
	c.Close()

	b, err := Open(parentDir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	stale := b.Resolver.OutOfDate(b.Graph)
	if len(stale) != 2 {
		t.Fatalf("both instances should be reported out of date, got %d", len(stale))
	}
	mustOK(t, b.ApplyTool("sync_component", []byte(`{}`), PolicyCommit))
	if len(b.Resolver.OutOfDate(b.Graph)) != 0 {
		t.Fatal("nothing should be out of date after a sync")
	}
	after := vol(t, b)
	if near(after, before) {
		t.Fatalf("the component edit should have changed the parent's geometry: %g", after)
	}
	if !near(after, 2*before) { // dz 10 -> 20 on both instances
		t.Fatalf("got %g want %g", after, 2*before)
	}
	// Syncing is a normal ChangeSet: undoing it restores the old pin, which makes the
	// instances out of date again. It does not restore the old geometry — v1 keeps no archive
	// of past component versions, so what is on disk is always what gets built. That is the
	// limitation explicit version locking will lift.
	if _, err := b.Undo(); err != nil {
		t.Fatal(err)
	}
	if len(b.Resolver.OutOfDate(b.Graph)) != 2 {
		t.Fatal("undoing a sync should put the instances back out of date")
	}
}

// A pinned content hash is what makes a component edit visible to the rebuild cache, the graph
// hash, checkpoints and the staleness check. Without it the parent would silently serve a
// cached body.
func TestInstancePinsComponentContentInTheGraphHash(t *testing.T) {
	lib := t.TempDir()
	a := newInLibrary(t, "car", lib)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"wheel","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Wheel","features":["wheel"]}`), PolicyCommit))

	var inst string
	for _, n := range a.Graph.Nodes {
		if n.Type == features.TypeComponentInstance {
			inst = n.ID
		}
	}
	hash, _ := a.Graph.Get(inst).Parameters[features.ParamContentHash].(string)
	if hash == "" {
		t.Fatal("the instance should pin the component's content hash")
	}
	before := a.Graph.GeometryHash()

	// Edit the component on disk, then sync: the project's own geometry hash has to move, or
	// the rebuild cache, checkpoints and the stale-selection check would all miss the change.
	compPath := a.Lib.List()[0].Path
	c, err := Open(compPath, clock)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, c.ApplyTool("modify_parameter", []byte(`{"feature":"wheel","parameter":"dz","value":25}`), PolicyCommit))
	if err := c.Save(compPath); err != nil {
		t.Fatal(err)
	}
	c.Close()

	mustOK(t, a.ApplyTool("sync_component", []byte(`{}`), PolicyCommit))
	if a.Graph.GeometryHash() == before {
		t.Fatal("re-pinning a component must change the project's geometry hash")
	}
	if got, _ := a.Graph.Get(inst).Parameters[features.ParamContentHash].(string); got == hash {
		t.Fatal("the pin should have moved to the component's new content")
	}

	// The reference itself is not an editable field: it is changed through the component tools.
	if out := a.ApplyTool("modify_parameter",
		[]byte(`{"feature":"`+inst+`","parameter":"content_hash","value":"deadbeef"}`), PolicyCommit); out.OK {
		t.Fatal("content_hash must not be editable as a plain parameter")
	}
	// Placement, on the other hand, is an ordinary parameter edit.
	mustOK(t, a.ApplyTool("modify_parameter",
		[]byte(`{"feature":"`+inst+`","parameter":"dx","value":12}`), PolicyCommit))
}

// Detaching turns the reference into local geometry that the component no longer drives.
func TestDetachComponentProducesIndependentGeometry(t *testing.T) {
	lib := t.TempDir()
	parentDir := filepath.Join(lib, "car.scadproj")
	a := newInLibrary(t, "car", lib)

	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"wheel","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Wheel","features":["wheel"]}`), PolicyCommit))
	var inst string
	for _, n := range a.Graph.Nodes {
		if n.Type == features.TypeComponentInstance {
			inst = n.ID
		}
	}
	before := vol(t, a)
	mustOK(t, a.ApplyTool("detach_component", []byte(`{"instance":"`+inst+`"}`), PolicyCommit))

	if instanceCount(a) != 0 {
		t.Fatal("the instance should be gone after detaching")
	}
	if got := vol(t, a); !near(got, before) {
		t.Fatalf("detaching must preserve the geometry: got %g want %g", got, before)
	}
	if err := a.Save(parentDir); err != nil {
		t.Fatal(err)
	}
	compPath := a.Lib.List()[0].Path
	a.Close()

	// Changing the component now has no effect on the detached copy.
	c, err := Open(compPath, clock)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, c.ApplyTool("modify_parameter", []byte(`{"feature":"wheel","parameter":"dz","value":40}`), PolicyCommit))
	if err := c.Save(compPath); err != nil {
		t.Fatal(err)
	}
	c.Close()

	b, err := Open(parentDir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got := vol(t, b); !near(got, before) {
		t.Fatalf("a detached body must not follow its old component: got %g want %g", got, before)
	}
}

// Components may contain components, to any depth. Only cycles are refused.
func TestRecursiveCompositionAndCycleRefusal(t *testing.T) {
	lib := t.TempDir()
	a := newInLibrary(t, "rim", lib)

	// hub -> rim (contains hub) -> wheel (contains rim)
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"hub","dx":4,"dy":4,"dz":4}`), PolicyAuto))
	hubVol := vol(t, a)
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Hub","features":["hub"]}`), PolicyCommit))
	if err := a.Save(filepath.Join(lib, "rim.scadproj")); err != nil {
		t.Fatal(err)
	}
	a.SetReusable(true, "a rim", nil)
	if err := a.Save(filepath.Join(lib, "rim.scadproj")); err != nil {
		t.Fatal(err)
	}
	rimID := a.Meta.ID
	a.Close()

	w := newInLibrary(t, "wheel", lib)
	defer w.Close()
	mustOK(t, w.ApplyTool("insert_component", []byte(`{"component":"`+rimID+`"}`), PolicyCommit))
	if got := vol(t, w); !near(got, hubVol) {
		t.Fatalf("a nested component should resolve through both levels: got %g want %g", got, hubVol)
	}

	// Inserting the rim into the hub would close the loop rim → hub → rim.
	hubPath := ""
	for _, c := range w.Lib.List() {
		if c.Name == "Hub" {
			hubPath = c.Path
		}
	}
	if hubPath == "" {
		t.Fatal("the hub component should be in the library")
	}
	h, err := Open(hubPath, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	out := h.ApplyTool("insert_component", []byte(`{"component":"`+rimID+`"}`), PolicyCommit)
	if out.OK {
		t.Fatal("a circular component reference must be refused")
	}
	if !strings.Contains(out.Error, "circular") {
		t.Fatalf("the refusal should name the cycle, got %q", out.Error)
	}
}

// A component has to be self-contained: the error must say exactly what crosses the boundary.
func TestCreateComponentRefusesAnOpenSelection(t *testing.T) {
	lib := t.TempDir()
	a := newInLibrary(t, "part", lib)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":50,"dy":50,"dz":5}`), PolicyAuto))
	mustOK(t, a.ApplyTool("add_hole", []byte(`{"id":"bore","target":"plate","diameter":4,"positions":[[10,10]]}`), PolicyAuto))

	out := a.ApplyTool("create_component", []byte(`{"name":"Plate","features":["plate"]}`), PolicyCommit)
	if out.OK {
		t.Fatal("a selection something else depends on must be refused")
	}
	if !strings.Contains(out.Error, "bore") {
		t.Fatalf("the error should name the dependent feature, got %q", out.Error)
	}
}

// components.json is derived from the instances, never maintained alongside them.
func TestComponentsManifestIsDerivedOnSave(t *testing.T) {
	lib := t.TempDir()
	dir := filepath.Join(lib, "car.scadproj")
	a := newInLibrary(t, "car", lib)
	defer a.Close()
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"wheel","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Wheel","features":["wheel"]}`), PolicyCommit))
	mustOK(t, a.ApplyTool("insert_component", []byte(`{"component":"Wheel","dx":50}`), PolicyCommit))
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "components.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Components []struct {
			ProjectID string   `json:"project_id"`
			Instances []string `json:"instances"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Components) != 1 || len(got.Components[0].Instances) != 2 {
		t.Fatalf("components.json should list one dependency with two instances: %s", b)
	}
}

// Nested component resolution must not leak OCCT shapes: every nested engine is reachable from
// the owning engine's Close.
func TestNoKernelLeaksWithComponents(t *testing.T) {
	lib := t.TempDir()
	a := newInLibrary(t, "car", lib)
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"wheel","dx":10,"dy":10,"dz":10}`), PolicyAuto))
	mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Wheel","features":["wheel"]}`), PolicyCommit))
	for i := 0; i < 4; i++ {
		mustOK(t, a.ApplyTool("insert_component", []byte(`{"component":"Wheel","dx":50}`), PolicyCommit))
	}
	if _, err := a.Model(); err != nil {
		t.Fatal(err)
	}
	a.Undo()
	a.Redo()
	a.Engine.Close()
	if n := a.K.LiveShapes(); n != 0 {
		t.Fatalf("leaked %d kernel shapes", n)
	}
	a.K.Close()
}

// A cycle must come back as an error, not hang: the engine is not reentrant, so entering a
// nested rebuild of a project already on the stack would deadlock.
func TestCycleDetectionDoesNotDeadlock(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		lib := t.TempDir()
		a := newInLibrary(t, "self", lib)
		defer a.Close()
		mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"b","dx":5,"dy":5,"dz":5}`), PolicyAuto))
		mustOK(t, a.ApplyTool("create_component", []byte(`{"name":"Self","features":["b"]}`), PolicyCommit))
		id := a.Lib.List()[0].ProjectID
		c, err := Open(a.Lib.List()[0].Path, clock)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		if out := c.ApplyTool("insert_component", []byte(`{"component":"`+id+`"}`), PolicyCommit); out.OK {
			t.Error("a component may not contain itself")
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("cycle detection deadlocked")
	}
}
