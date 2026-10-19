package features

import (
	"fmt"

	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/meshimport"
)

func init() {
	register(&Feature{Type: TypeImportMesh, Build: buildImportMesh})
}

// TypeImportMesh is the node type for a triangle mesh brought in from outside (STL). Its
// geometry is not described by parameters but by a stored, validated mesh referenced by id; the
// id is the hash of the mesh's content, so it is part of the node's content key, the rebuild
// cache key and the graph hash. Parameters:
//
//	mesh       the mesh id (mesh_<hash>), from meshimport.Prepare
//	units      the file's length unit: mm (default), cm, m, in, ft
//	placement  file (as authored, default), center (bounding box centred on the origin) or
//	           base (centred on X/Y, resting on Z = 0)
//
// The body is then moved, rotated or scaled like any other with the transform tool.
const TypeImportMesh = "import_mesh"

// ParamMesh is the parameter holding the mesh id.
const ParamMesh = "mesh"

// MeshUnits maps a unit name to millimetres.
var MeshUnits = map[string]float64{"mm": 1, "cm": 10, "m": 1000, "in": 25.4, "ft": 304.8}

// MeshPlacements lists the accepted placement values.
var MeshPlacements = []string{"file", "center", "base"}

// MeshResolver turns a mesh id into its stored, already validated mesh.
type MeshResolver interface {
	Mesh(id string) (*meshimport.Mesh, error)
}

func buildImportMesh(c *Ctx) (*api.Shape, error) {
	id := c.P.Str(ParamMesh)
	if !meshimport.ValidID(id) {
		return nil, fmt.Errorf("import_mesh needs a %q parameter holding a mesh id (mesh_<hash>), got %q", ParamMesh, id)
	}
	unit := c.P.Str("units")
	if unit == "" {
		unit = "mm"
	}
	f, ok := MeshUnits[unit]
	if !ok {
		return nil, fmt.Errorf("unknown units %q (use mm, cm, m, in or ft)", unit)
	}
	placement := c.P.Str("placement")
	if placement == "" {
		placement = "file"
	}
	if placement != "file" && placement != "center" && placement != "base" {
		return nil, fmt.Errorf("unknown placement %q (use file, center or base)", placement)
	}
	if c.Meshes == nil {
		return nil, fmt.Errorf("no mesh store is configured, cannot resolve mesh %q", id)
	}
	m, err := c.Meshes.Mesh(id)
	if err != nil {
		return nil, err
	}
	lo, hi := meshimport.Bounds(m)
	var off [3]float64
	switch placement {
	case "center":
		for k := 0; k < 3; k++ {
			off[k] = -(lo[k] + hi[k]) / 2
		}
	case "base":
		off = [3]float64{-(lo[0] + hi[0]) / 2, -(lo[1] + hi[1]) / 2, -lo[2]}
	}
	pos := make([]float64, len(m.Positions))
	for i, v := range m.Positions {
		pos[i] = (float64(v) + off[i%3]) * f
	}
	return c.K.ImportMesh(pos, m.Indices, meshimport.Volume(m)*f*f*f)
}
