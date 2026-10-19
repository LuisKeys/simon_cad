package ui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/meshimport"
)

// maxMeshBytes bounds an uploaded STL.
const maxMeshBytes = meshimport.MaxBytes

// meshBuildMicros is the measured cost, in microseconds, of building one triangle into a solid
// (28 s for 72 000 triangles): it only sizes the progress bar.
const meshBuildMicros = 450

// ImportSTL validates an uploaded STL file and adds it as a body. The file is checked before
// anything is kept: a mesh that is not one closed, consistently oriented, non-self-intersecting
// solid is refused with what is wrong and where, and the model is not touched. An accepted
// mesh goes in through the import_stl tool, like any other edit. With fillHoles the holes of the
// file are closed first; a file refused only for its holes comes back as a *meshimport.Error with
// HolesOnly set, so the caller can offer that.
func (s *Service) ImportSTL(name string, data []byte, units, placement string, fillHoles bool, decimateTo int) (Outcome, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if !strings.EqualFold(filepath.Ext(name), ".stl") {
		return Outcome{}, fmt.Errorf("%q is not an .stl file", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return Outcome{}, err
	}
	stage := func(text string, etaMS int) {
		s.hub.Publish(Notice{Kind: "progress", Progress: &Progress{Op: "import", Stage: text, ETAMS: etaMS}})
	}
	check := "Reading and checking the mesh"
	if fillHoles {
		check = "Reading the mesh, closing its holes and checking it"
	}
	stage(check, 0)
	p, err := s.app.StoreMesh(data, meshimport.Options{FillHoles: fillHoles, DecimateTo: decimateTo})
	if err != nil {
		return Outcome{}, fmt.Errorf("%s was not imported: %w", name, err)
	}
	// Turning the triangles into a kernel solid is the long part: about half a millisecond each.
	stage(fmt.Sprintf("Building the solid from %d triangles", p.Report.Triangles), 300+p.Report.Triangles*meshBuildMicros/1000)
	args := map[string]any{"mesh": p.ID, "name": strings.TrimSuffix(name, filepath.Ext(name)), "filename": name}
	if units != "" {
		args["units"] = units
	}
	if placement != "" {
		args["placement"] = placement
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return Outcome{}, err
	}
	// Choosing a file to import is the user's explicit act, so the body is added at once (undoable),
	// whatever the review policy for the agent's proposals is.
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	return s.finishTool("import_stl", s.app.ApplyTool("import_stl", raw, app.PolicyAuto)), nil
}

// decimateParam reads the decimate_to query value: 0 (no reduction) when absent or not a number.
func decimateParam(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
