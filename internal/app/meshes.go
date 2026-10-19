package app

import (
	"fmt"
	"sync"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/meshimport"
	"github.com/accelone/simoncad/internal/persistence"
)

// Imported meshes. A mesh enters the project only through meshimport.Prepare, which refuses
// anything that is not one closed, consistently oriented, non-self-intersecting solid, and it is
// kept in its canonical form under assets/meshes/<id>.stl. import_mesh nodes reference it by id.
// Like attachments, a mesh lives in memory until the project has a directory; Save writes those
// the graph still references and drops the rest.

type meshStore struct {
	mu      sync.Mutex
	data    map[string][]byte
	decoded map[string]*meshimport.Mesh
}

func newMeshStore() *meshStore {
	return &meshStore{data: map[string][]byte{}, decoded: map[string]*meshimport.Mesh{}}
}

// StoreMesh validates an STL file and keeps the accepted mesh; the options close its holes first
// and/or reduce its triangles after (the result is validated all the same). A rejected file
// returns the *meshimport.Error and leaves the project untouched.
func (a *App) StoreMesh(data []byte, opt meshimport.Options) (*meshimport.Prepared, error) {
	p, err := meshimport.Prepare(data, opt)
	if err != nil {
		return nil, err
	}
	a.meshes.mu.Lock()
	a.meshes.data[p.ID] = p.Data
	a.meshes.decoded[p.ID] = p.Mesh
	a.meshes.mu.Unlock()
	return p, nil
}

// ReduceMesh decimates a stored mesh to about target triangles and keeps the result as a new
// mesh (the original stays, so undo can go back to it).
func (a *App) ReduceMesh(id string, target int) (*meshimport.Prepared, error) {
	m, err := a.Mesh(id)
	if err != nil {
		return nil, err
	}
	p, err := meshimport.Reduce(m, target)
	if err != nil {
		return nil, err
	}
	a.meshes.mu.Lock()
	a.meshes.data[p.ID] = p.Data
	a.meshes.decoded[p.ID] = p.Mesh
	a.meshes.mu.Unlock()
	return p, nil
}

// Mesh returns a stored mesh (features.MeshResolver). The stored bytes are checked against the
// id every time they are decoded, so a corrupted store cannot change the model silently.
func (a *App) Mesh(id string) (*meshimport.Mesh, error) {
	a.meshes.mu.Lock()
	defer a.meshes.mu.Unlock()
	if m, ok := a.meshes.decoded[id]; ok {
		return m, nil
	}
	data, ok := a.meshes.data[id]
	if !ok {
		return nil, fmt.Errorf("mesh %s is not in this project (its file was not imported or has been removed)", id)
	}
	m, err := meshimport.Decode(id, data)
	if err != nil {
		return nil, err
	}
	a.meshes.decoded[id] = m
	return m, nil
}

// MeshInfo describes a stored mesh.
func (a *App) MeshInfo(id string) (meshimport.Report, bool) {
	m, err := a.Mesh(id)
	if err != nil {
		return meshimport.Report{}, false
	}
	lo, hi := meshimport.Bounds(m)
	return meshimport.Report{Format: "binary", Triangles: m.Triangles(), Vertices: len(m.Positions) / 3,
		Min: lo, Max: hi, Volume: meshimport.Volume(m)}, true
}

// savedMeshes lists what a save stores: the meshes the graph's nodes reference.
func (a *App) savedMeshes() []persistence.Mesh { return a.meshesFor(a.Graph.Nodes) }

// meshesFor lists the stored meshes the nodes reference (a published component carries its own).
func (a *App) meshesFor(nodes []*cadir.Node) []persistence.Mesh {
	a.meshes.mu.Lock()
	defer a.meshes.mu.Unlock()
	seen := map[string]bool{}
	var out []persistence.Mesh
	for _, n := range nodes {
		id := meshID(n)
		if id == "" || seen[id] {
			continue
		}
		if data, ok := a.meshes.data[id]; ok {
			seen[id] = true
			out = append(out, persistence.Mesh{ID: id, Data: data})
		}
	}
	return out
}

func meshID(n *cadir.Node) string {
	if n.Type != features.TypeImportMesh {
		return ""
	}
	id, _ := n.Parameters[features.ParamMesh].(string)
	return id
}

// loadMeshes puts a loaded project's stored meshes in the store.
func (a *App) loadMeshes(p *persistence.Project) {
	a.meshes.mu.Lock()
	defer a.meshes.mu.Unlock()
	for _, m := range p.Meshes {
		a.meshes.data[m.ID] = m.Data
	}
}
