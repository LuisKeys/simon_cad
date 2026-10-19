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

// ImportSTL validates an uploaded STL, OBJ or 3MF file and adds it as a body. The file is checked before
// anything is kept: a mesh that is not one closed, consistently oriented, non-self-intersecting
// solid is refused with what is wrong and where, and the model is not touched. An accepted
// mesh goes in through the import_stl tool, like any other edit. With fillHoles the holes of the
// file are closed first; a file refused only for its holes comes back as a *meshimport.Error with
// HolesOnly set, so the caller can offer that.
func (s *Service) ImportSTL(name string, data []byte, units, placement string, fillHoles bool, decimateTo int) (Outcome, error) {
	name = filepath.Base(strings.TrimSpace(name))
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".stl" && ext != ".obj" && ext != ".3mf" {
		return Outcome{}, fmt.Errorf("%q is not an .stl, .obj or .3mf file", name)
	}
	tool := "import_stl"
	if ext == ".obj" {
		tool = "import_obj" // no units either: asked like an STL's
	}
	if ext == ".3mf" {
		tool = "import_3mf" // states its own unit: converted to mm on reading
		units = ""
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
	return s.finishTool(tool, s.app.ApplyTool(tool, raw, app.PolicyAuto)), nil
}

// decimateParam reads the decimate_to query value: 0 (no reduction) when absent or not a number.
func decimateParam(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ImportSTEP checks an uploaded STEP file and adds it as a body through the import_step tool. The
// kernel reads it first: a file that is unreadable, invalid or without a solid is refused with
// the reason and the model is not touched. STEP carries its own units, so there is nothing to ask.
func (s *Service) ImportSTEP(name string, data []byte, placement string) (Outcome, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if ext := strings.ToLower(filepath.Ext(name)); ext != ".step" && ext != ".stp" {
		return Outcome{}, fmt.Errorf("%q is not a .step or .stp file", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return Outcome{}, err
	}
	s.hub.Publish(Notice{Kind: "progress", Progress: &Progress{Op: "import", Stage: "Reading the STEP file and checking its solids"}})
	p, err := s.app.StoreStep(data)
	if err != nil {
		return Outcome{}, fmt.Errorf("%s was not imported: %w", name, err)
	}
	args := map[string]any{"step": p.ID, "name": strings.TrimSuffix(name, filepath.Ext(name)), "filename": name}
	if placement != "" {
		args["placement"] = placement
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return Outcome{}, err
	}
	// Choosing a file to import is the user's explicit act, so the body is added at once (undoable).
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	return s.finishTool("import_step", s.app.ApplyTool("import_step", raw, app.PolicyAuto)), nil
}
