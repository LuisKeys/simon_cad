package app

import (
	"fmt"
	"sync"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/persistence"
	"github.com/accelone/simoncad/internal/stepimport"
)

// Imported STEP files. A file enters the project only through stepimport.Prepare, which refuses
// anything the kernel does not read as valid solid geometry, and is kept byte for byte under
// assets/steps/<id>.step. import_step nodes reference it by id. Like meshes, a file lives in
// memory until the project has a directory; Save writes those the graph still references and
// drops the rest.

type stepStore struct {
	mu      sync.Mutex
	data    map[string][]byte
	reports map[string]stepimport.Report // what the kernel found when a file was checked
}

func newStepStore() *stepStore {
	return &stepStore{data: map[string][]byte{}, reports: map[string]stepimport.Report{}}
}

// StoreStep checks a STEP file and keeps it. A rejected file returns the reason and leaves the
// project untouched.
func (a *App) StoreStep(data []byte) (*stepimport.Prepared, error) {
	p, err := stepimport.Prepare(a.K, data)
	if err != nil {
		return nil, err
	}
	a.steps.mu.Lock()
	a.steps.data[p.ID] = p.Data
	a.steps.reports[p.ID] = p.Report
	a.steps.mu.Unlock()
	return p, nil
}

// Step returns a stored file's bytes (features.StepResolver); the rebuild checks them against the id.
func (a *App) Step(id string) ([]byte, error) {
	a.steps.mu.Lock()
	defer a.steps.mu.Unlock()
	data, ok := a.steps.data[id]
	if !ok {
		return nil, fmt.Errorf("STEP file %s is not in this project (it was not imported or has been removed)", id)
	}
	return data, nil
}

// StepInfo describes a stored STEP file.
func (a *App) StepInfo(id string) (stepimport.Report, bool) {
	a.steps.mu.Lock()
	r, ok := a.steps.reports[id]
	a.steps.mu.Unlock()
	if ok {
		return r, true
	}
	data, err := a.Step(id)
	if err != nil {
		return stepimport.Report{}, false
	}
	p, err := stepimport.Prepare(a.K, data)
	if err != nil {
		return stepimport.Report{}, false
	}
	a.steps.mu.Lock()
	a.steps.reports[id] = p.Report
	a.steps.mu.Unlock()
	return p.Report, true
}

// savedSteps lists what a save stores: the files the graph's nodes reference.
func (a *App) savedSteps() []persistence.Step { return a.stepsFor(a.Graph.Nodes) }

// stepsFor lists the stored files the nodes reference (a published component carries its own).
func (a *App) stepsFor(nodes []*cadir.Node) []persistence.Step {
	a.steps.mu.Lock()
	defer a.steps.mu.Unlock()
	seen := map[string]bool{}
	var out []persistence.Step
	for _, n := range nodes {
		if n.Type != features.TypeImportStep {
			continue
		}
		id, _ := n.Parameters[features.ParamStep].(string)
		if data, ok := a.steps.data[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, persistence.Step{ID: id, Data: data})
		}
	}
	return out
}

// loadSteps puts a loaded project's stored STEP files in the store.
func (a *App) loadSteps(p *persistence.Project) {
	a.steps.mu.Lock()
	defer a.steps.mu.Unlock()
	for _, s := range p.Steps {
		a.steps.data[s.ID] = s.Data
	}
}
