package gcode

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// synthetic builds PrusaSlicer-shaped G-code with n extrusion moves over layers of 2000 moves.
func synthetic(n int) string {
	var b strings.Builder
	b.Grow(n * 32)
	b.WriteString("M83\nG90\nG1 X0 Y0 Z0.2 F6000\n")
	for i := 0; i < n; i++ {
		if i%2000 == 0 {
			fmt.Fprintf(&b, ";LAYER_CHANGE\n;Z:%.2f\nG1 Z%.2f F600\n;TYPE:External perimeter\n", 0.2*float64(i/2000+1), 0.2*float64(i/2000+1))
		}
		fmt.Fprintf(&b, "G1 X%.3f Y%.3f E%.5f F1800\n", float64(i%200)+0.123, float64(i%173)+0.456, 0.0123)
	}
	return b.String()
}

func BenchmarkParse(b *testing.B) {
	for _, n := range []int{100_000, 500_000, 1_000_000} {
		src := synthetic(n)
		b.Run(fmt.Sprintf("%dk_moves", n/1000), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := Parse(context.Background(), strings.NewReader(src), Options{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
