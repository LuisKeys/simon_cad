package features

import (
	"fmt"

	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/stepimport"
)

func init() {
	register(&Feature{Type: TypeImportStep, Build: buildImportStep})
}

// TypeImportStep is the node type for a STEP file brought in from outside. Unlike import_mesh it
// keeps the exact B-Rep of the file (real cylinders, planes and B-spline surfaces), so fillets,
// holes and face selectors work on it as on any modelled body. The geometry is not described by
// parameters but by a stored file referenced by id; the id is the hash of the file's content, so
// it is part of the node's content key, the rebuild cache key and the graph hash. Parameters:
//
//	step       the file id (step_<hash>), from stepimport.Prepare
//	placement  file (as authored, default), center (bounding box centred on the origin) or
//	           base (centred on X/Y, resting on Z = 0)
//
// A STEP file declares its own length unit and the reader converts it to millimetres, so there
// is no units parameter. The body is then moved, rotated or scaled with the transform tool.
const TypeImportStep = "import_step"

// ParamStep is the parameter holding the STEP file id.
const ParamStep = "step"

// StepResolver turns a STEP id into the bytes of its stored file.
type StepResolver interface {
	Step(id string) ([]byte, error)
}

func buildImportStep(c *Ctx) (*api.Shape, error) {
	id := c.P.Str(ParamStep)
	if !stepimport.ValidID(id) {
		return nil, fmt.Errorf("import_step needs a %q parameter holding a STEP id (step_<hash>), got %q", ParamStep, id)
	}
	placement := c.P.Str("placement")
	if placement == "" {
		placement = "file"
	}
	if placement != "file" && placement != "center" && placement != "base" {
		return nil, fmt.Errorf("unknown placement %q (use file, center or base)", placement)
	}
	if c.Steps == nil {
		return nil, fmt.Errorf("no STEP store is configured, cannot resolve %q", id)
	}
	data, err := c.Steps.Step(id)
	if err != nil {
		return nil, err
	}
	s, err := stepimport.Load(c.K, id, data)
	if err != nil {
		return nil, err
	}
	if placement == "file" {
		return s, nil
	}
	p, err := s.Props()
	if err != nil {
		s.Release()
		return nil, err
	}
	dx, dy, dz := -(p.MinX+p.MaxX)/2, -(p.MinY+p.MaxY)/2, -(p.MinZ+p.MaxZ)/2
	if placement == "base" {
		dz = -p.MinZ
	}
	moved, err := s.Translate(dx, dy, dz)
	s.Release()
	return moved, err
}
