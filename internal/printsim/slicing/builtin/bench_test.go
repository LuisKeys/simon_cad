package builtin

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/slicing"
	"github.com/accelone/simoncad/internal/printsim/testmesh"
	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

// BenchmarkSlice slices a hollow cylinder (a typical bracket-like part) at several heights.
func BenchmarkSlice(b *testing.B) {
	for _, h := range []float32{20, 60, 120} {
		path := filepath.Join(b.TempDir(), "m.stl")
		tris := testmesh.Prism(testmesh.Circle(110, 110, 40, 180), testmesh.Circle(110, 110, 25, 180), 0, h)
		if err := testmesh.WriteSTL(path, tris); err != nil {
			b.Fatal(err)
		}
		p := profile.GenericFDM()
		var segs int
		b.Run(fmt.Sprintf("tube_80mm_h%.0f", h), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r, err := Slicer{}.Slice(context.Background(), slicing.Input{STLPath: path}, p)
				if err != nil {
					b.Fatal(err)
				}
				r.Finalize(toolpath.FinalizeParams{FilamentDiameter: 1.75, FilamentDensity: 1.24, LayerHeight: 0.2, Nozzle: 0.4})
				segs = len(r.Segments)
			}
			b.ReportMetric(float64(segs), "segments")
		})
	}
}
