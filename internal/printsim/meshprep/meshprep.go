// Package meshprep turns the CAD result into a bed-placed STL for slicing. The CAD
// geometry is never modified: the kernel exports the shape as-is, and the translation onto
// the build plate is applied to the exported copy.
package meshprep

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/accelone/simoncad/internal/export"
	"github.com/accelone/simoncad/internal/printsim/profile"
)

// Exporter writes the model as a binary STL at the given linear deflection (mm).
// *geometry/api.Shape satisfies it.
type Exporter interface {
	ExportSTL(path string, deflection float64) error
}

// Deflection is the tessellation tolerance used for slicing (mm).
const Deflection = 0.05

// maxSTLBytes bounds the exported mesh we are willing to load (≈ 5M triangles).
const maxSTLBytes = 256 << 20

// Placement is the translation applied to CAD coordinates to land the model on the bed:
// bed = cad + (DX, DY, DZ). The front end applies the same offset to the original model
// when it overlays it on the printer.
type Placement struct {
	DX, DY, DZ float64
}

// Prepared is a bed-placed STL ready for a slicer.
type Prepared struct {
	STLPath   string
	Placement Placement
	Min, Max  [3]float64 // bounds in bed coordinates
	Report    export.STLReport
}

// Size is the extent of the model.
func (p Prepared) Size() [3]float64 {
	return [3]float64{p.Max[0] - p.Min[0], p.Max[1] - p.Min[1], p.Max[2] - p.Min[2]}
}

// Error is a user-facing preparation failure.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Prepare exports src into dir, validates it, centres it on the bed with its lowest point
// on the plate and writes dir/model.stl.
func Prepare(ctx context.Context, src Exporter, p profile.PrintProfile, dir string) (*Prepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw := filepath.Join(dir, "model_raw.stl")
	if err := src.ExportSTL(raw, Deflection); err != nil {
		return nil, fmt.Errorf("could not mesh the model: %w", err)
	}
	defer os.Remove(raw)
	if st, err := os.Stat(raw); err != nil || st.Size() > maxSTLBytes {
		return nil, &Error{"the model's mesh is too large to slice (over 256 MiB); simplify the model"}
	}
	rep, err := export.CheckBinarySTL(raw)
	if err != nil {
		return nil, fmt.Errorf("the exported mesh is not readable: %w", err)
	}
	if rep.Triangles == 0 {
		return nil, &Error{"the model has no geometry to print"}
	}
	if !rep.Watertight() {
		return nil, &Error{fmt.Sprintf("the model's mesh is not watertight (%d open edges, %d non-manifold edges), so it cannot be sliced reliably", rep.OpenEdges, rep.NonManifold)}
	}
	if rep.SignedVolume <= 0 {
		return nil, &Error{"the model's mesh is inside-out (negative volume)"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(raw)
	if err != nil {
		return nil, err
	}
	min, max := bounds(data)
	pl := Placement{
		DX: p.BuildX/2 - (min[0]+max[0])/2,
		DY: p.BuildY/2 - (min[1]+max[1])/2,
		DZ: -min[2],
	}
	out := &Prepared{STLPath: filepath.Join(dir, "model.stl"), Placement: pl, Report: rep}
	for a, d := range [3]float64{pl.DX, pl.DY, pl.DZ} {
		out.Min[a], out.Max[a] = min[a]+d, max[a]+d
	}
	size := out.Size()
	if size[0] > p.BuildX+1e-6 || size[1] > p.BuildY+1e-6 || size[2] > p.BuildZ+1e-6 {
		return nil, &Error{fmt.Sprintf("the model (%.1f × %.1f × %.1f mm) does not fit the build volume (%.0f × %.0f × %.0f mm)",
			size[0], size[1], size[2], p.BuildX, p.BuildY, p.BuildZ)}
	}
	translate(data, pl)
	if err := os.WriteFile(out.STLPath, data, 0o600); err != nil {
		return nil, err
	}
	return out, nil
}

func f32(b []byte) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))) }

// vertices calls fn with the byte offset of every vertex of a binary STL.
func vertices(data []byte, fn func(off int)) {
	n := int(binary.LittleEndian.Uint32(data[80:84]))
	for i := 0; i < n; i++ {
		base := 84 + i*50 + 12
		for j := 0; j < 3; j++ {
			fn(base + j*12)
		}
	}
}

func bounds(data []byte) (min, max [3]float64) {
	min = [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	max = [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	vertices(data, func(o int) {
		for a := 0; a < 3; a++ {
			v := f32(data[o+4*a:])
			if v < min[a] {
				min[a] = v
			}
			if v > max[a] {
				max[a] = v
			}
		}
	})
	return
}

func translate(data []byte, p Placement) {
	d := [3]float64{p.DX, p.DY, p.DZ}
	vertices(data, func(o int) {
		for a := 0; a < 3; a++ {
			v := f32(data[o+4*a:]) + d[a]
			binary.LittleEndian.PutUint32(data[o+4*a:], math.Float32bits(float32(v)))
		}
	})
}

// ReadTriangles loads a binary STL as flat float32 triples (9 per triangle).
func ReadTriangles(path string) ([]float32, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxSTLBytes {
		return nil, &Error{"mesh file is too large"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 84 {
		return nil, &Error{"not a binary STL"}
	}
	n := int(binary.LittleEndian.Uint32(data[80:84]))
	if len(data) != 84+n*50 {
		return nil, &Error{"not a binary STL"}
	}
	tri := make([]float32, 0, n*9)
	for i := 0; i < n; i++ {
		base := 84 + i*50 + 12
		for j := 0; j < 9; j++ {
			tri = append(tri, math.Float32frombits(binary.LittleEndian.Uint32(data[base+4*j:])))
		}
	}
	return tri, nil
}
