// Package stepimport is the gate for STEP files brought in from outside. A STEP file keeps its
// exact B-Rep (planes, cylinders, B-spline surfaces), so unlike a mesh the kernel reads it as it
// is; what is checked is that it holds at least one solid and that the kernel judges the result
// valid. Accepted files are kept byte for byte under a content id, so a stored file that no
// longer matches its id is refused on use instead of silently changing the model.
package stepimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/accelone/simoncad/internal/geometry/api"
)

const (
	// MaxBytes bounds the size of a file that is even looked at.
	MaxBytes = 64 << 20
	// IDPrefix starts every STEP id.
	IDPrefix = "step_"
	// minVolume is the volume (mm³) under which a solid counts as empty.
	minVolume = 1e-9
)

// ValidID reports whether id has the shape of a STEP id, so it is safe to use in a file name.
func ValidID(id string) bool {
	h, ok := strings.CutPrefix(id, IDPrefix)
	if !ok || len(h) != 24 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil && h == strings.ToLower(h)
}

// ID is the content id of a STEP file.
func ID(data []byte) string {
	sum := sha256.Sum256(data)
	return IDPrefix + hex.EncodeToString(sum[:12])
}

// Report describes an accepted file. Lengths are millimetres: the reader converts whatever unit
// the file declares.
type Report struct {
	Solids   int
	Faces    int
	Volume   float64
	Min, Max [3]float64
}

// Size is the bounding box extent.
func (r Report) Size() [3]float64 {
	return [3]float64{r.Max[0] - r.Min[0], r.Max[1] - r.Min[1], r.Max[2] - r.Min[2]}
}

// Prepared is an accepted file.
type Prepared struct {
	ID     string
	Data   []byte
	Report Report
}

// Prepare checks that data is a STEP file the kernel reads as valid solid geometry. Nothing is
// kept: the caller stores Prepared.Data when it is accepted.
func Prepare(k *api.Kernel, data []byte) (*Prepared, error) {
	if err := checkHeader(data); err != nil {
		return nil, err
	}
	s, err := read(k, data)
	if err != nil {
		return nil, err
	}
	defer s.Release()
	rep, err := inspect(s)
	if err != nil {
		return nil, err
	}
	return &Prepared{ID: ID(data), Data: data, Report: rep}, nil
}

// Load reads a stored file back into the kernel after checking it still is the file the id names.
func Load(k *api.Kernel, id string, data []byte) (*api.Shape, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("%q is not a STEP id", id)
	}
	if got := ID(data); got != id {
		return nil, fmt.Errorf("stored STEP file %s has been modified (its content is %s): refusing to use it", id, got)
	}
	return read(k, data)
}

func checkHeader(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("the file is empty")
	}
	if len(data) > MaxBytes {
		return fmt.Errorf("the file is larger than %d MB", MaxBytes>>20)
	}
	head := data[:min(len(data), 512)]
	if !bytes.Contains(head, []byte("ISO-10303-21")) {
		return fmt.Errorf("this is not a STEP file (it does not start with ISO-10303-21)")
	}
	return nil
}

// read gives the kernel the bytes through a private temporary file: its reader takes a path.
func read(k *api.Kernel, data []byte) (*api.Shape, error) {
	dir, err := os.MkdirTemp("", "simoncad-step-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "in.step")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return nil, err
	}
	s, err := k.ImportSTEP(p)
	if err != nil {
		return nil, fmt.Errorf("the kernel could not read the STEP file: %w", err)
	}
	return s, nil
}

func inspect(s *api.Shape) (Report, error) {
	ok, err := s.IsValid()
	if err != nil {
		return Report{}, err
	}
	if !ok {
		return Report{}, fmt.Errorf("the geometry in the file is not valid (self-intersecting, open or inconsistent faces): it cannot be used as a solid")
	}
	p, err := s.Props()
	if err != nil {
		return Report{}, err
	}
	if p.Solids == 0 {
		return Report{}, fmt.Errorf("the file has no solid: it holds only surfaces, curves or points, and a body cannot be made from that")
	}
	if p.Volume < minVolume {
		return Report{}, fmt.Errorf("the solids in the file have no volume (%.3g mm³)", p.Volume)
	}
	return Report{Solids: p.Solids, Faces: p.Faces, Volume: p.Volume,
		Min: [3]float64{p.MinX, p.MinY, p.MinZ}, Max: [3]float64{p.MaxX, p.MaxY, p.MaxZ}}, nil
}
