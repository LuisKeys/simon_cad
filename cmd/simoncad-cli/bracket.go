package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/accelone/simoncad/internal/export"
	"github.com/accelone/simoncad/internal/geometry/api"
)

// Phase 0 validation part: a rectangular MG996R servo bracket plate with a servo
// pocket and four M3 mounting holes. Dimensions in mm.
const (
	plateW, plateD, plateT = 70.0, 40.0, 4.0
	servoL, servoW         = 41.0, 20.5 // MG996R body pocket
	earPitchX, earPitchY   = 49.5, 10.0 // mounting-ear hole pattern
	m3Clearance            = 3.2
	cornerFillet           = 1.5
)

func runBracket(args []string) error {
	fs := newFlags("bracket")
	out := fs.String("out", "out", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	k := api.New()
	defer k.Close()

	var live []*api.Shape
	track := func(s *api.Shape, err error) (*api.Shape, error) {
		if s != nil {
			live = append(live, s)
		}
		return s, err
	}
	defer func() {
		for _, s := range live {
			s.Release()
		}
	}()

	plate, err := track(k.Box(plateW, plateD, plateT))
	if err != nil {
		return err
	}
	plate, err = track(plate.Fillet(cornerFillet))
	if err != nil {
		return err
	}

	// Servo pocket, centred, cut through the plate.
	pocket, err := track(k.Box(servoL, servoW, plateT+2))
	if err != nil {
		return err
	}
	pocket, err = track(pocket.Translate((plateW-servoL)/2, (plateD-servoW)/2, -1))
	if err != nil {
		return err
	}
	part, err := track(k.Subtract(plate, pocket))
	if err != nil {
		return err
	}

	// Four M3 holes at the ear pattern.
	for _, sx := range []float64{-1, 1} {
		for _, sy := range []float64{-1, 1} {
			hole, err := track(k.Cylinder(m3Clearance/2, plateT+2))
			if err != nil {
				return err
			}
			hole, err = track(hole.Translate(plateW/2+sx*earPitchX/2, plateD/2+sy*earPitchY/2, -1))
			if err != nil {
				return err
			}
			if part, err = track(k.Subtract(part, hole)); err != nil {
				return err
			}
		}
	}

	valid, err := part.IsValid()
	if err != nil {
		return err
	}
	props, err := part.Props()
	if err != nil {
		return err
	}
	stepPath := filepath.Join(*out, "servo_bracket.step")
	stlPath := filepath.Join(*out, "servo_bracket.stl")
	if err := part.ExportSTEP(stepPath); err != nil {
		return err
	}
	if err := part.ExportSTL(stlPath, 0.05); err != nil {
		return err
	}

	// Verification: STEP round-trip volume and STL watertightness.
	re, err := track(k.ImportSTEP(stepPath))
	if err != nil {
		return err
	}
	reProps, err := re.Props()
	if err != nil {
		return err
	}
	stl, err := export.CheckBinarySTL(stlPath)
	if err != nil {
		return err
	}

	relErr := math.Abs(reProps.Volume-props.Volume) / props.Volume
	fmt.Printf("OCCT %s\n", k.Version())
	fmt.Printf("valid solid:      %v\n", valid)
	fmt.Printf("volume / area:    %.3f mm³ / %.3f mm²\n", props.Volume, props.Area)
	fmt.Printf("bbox:             %.2f x %.2f x %.2f mm\n", props.MaxX-props.MinX, props.MaxY-props.MinY, props.MaxZ-props.MinZ)
	fmt.Printf("topology:         %d solid, %d faces, %d edges\n", props.Solids, props.Faces, props.Edges)
	fmt.Printf("STEP round-trip:  volume rel. error %.2e\n", relErr)
	fmt.Printf("STL:              %d triangles (%d degenerate ignored), watertight=%v (open=%d, non-manifold=%d)\n",
		stl.Triangles, stl.Degenerate, stl.Watertight(), stl.OpenEdges, stl.NonManifold)

	switch {
	case !valid:
		return fmt.Errorf("B-Rep is not valid")
	case relErr > 1e-6:
		return fmt.Errorf("STEP round-trip volume mismatch: %.2e", relErr)
	case !stl.Watertight():
		return fmt.Errorf("STL is not watertight")
	}
	fmt.Println("OK")
	return nil
}
