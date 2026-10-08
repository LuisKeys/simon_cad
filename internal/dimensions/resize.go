package dimensions

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Anchors are the fixed points a dimension edit can keep in place: the center of the
// object's box (default) or the object origin. Face and vertex anchors may come later.
var Anchors = []string{"center", "object_origin"}

const (
	// MinExtent is the smallest extent that can be resized: below it the object is flat
	// along that axis and a scale factor (target / current) is undefined.
	MinExtent = 1e-6
	// MaxFactor bounds one edit, as the scale feature does.
	MaxFactor = 1000.0
)

// Factors is the MeshResizeService's calculation: the per-axis scale factors that take the
// current extents to the requested ones. target maps a local axis (0 = width, 1 = depth,
// 2 = height) to its new extent in mm; axes not in target keep their extent. With
// proportional set and exactly one requested axis, the other two follow with the same
// factor (lock proportions).
func Factors(cur [3]float64, target map[int]float64, proportional bool) ([3]float64, error) {
	f := [3]float64{1, 1, 1}
	if len(target) == 0 {
		return f, fmt.Errorf("give at least one dimension")
	}
	axes := make([]int, 0, len(target))
	for i := range target {
		axes = append(axes, i)
	}
	sort.Ints(axes)
	for _, i := range axes {
		v := target[i]
		if i < 0 || i > 2 {
			return f, fmt.Errorf("unknown axis %d", i)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || !(v > 0) {
			return f, fmt.Errorf("%s must be a positive number of mm (got %v)", Axes[i], v)
		}
		if cur[i] < MinExtent {
			return f, fmt.Errorf("the object has no %s (it is flat along its local %s axis), so it cannot be resized along it", Axes[i], "XYZ"[i:i+1])
		}
		f[i] = v / cur[i]
	}
	if proportional && len(axes) == 1 {
		k := f[axes[0]]
		f = [3]float64{k, k, k}
	}
	for i, v := range f {
		if v > MaxFactor || v < 1/MaxFactor {
			return f, fmt.Errorf("changing %s from %s to %s mm is a factor of %s; one edit can scale by at most %g×",
				Axes[i], fmtMM(cur[i]), fmtMM(cur[i]*v), fmtMM(v), MaxFactor)
		}
	}
	if Identity(f) {
		var parts []string
		for _, i := range axes {
			parts = append(parts, fmt.Sprintf("%s is already %s mm", Axes[i], fmtMM(cur[i])))
		}
		return f, fmt.Errorf("nothing to change: %s", strings.Join(parts, ", "))
	}
	return f, nil
}

// Identity reports whether factors leave every extent unchanged (to 1e-9).
func Identity(f [3]float64) bool {
	for _, v := range f {
		if math.Abs(v-1) > 1e-9 {
			return false
		}
	}
	return true
}

// Resized returns the extents after applying factors.
func Resized(cur, f [3]float64) [3]float64 {
	return [3]float64{r6(cur[0] * f[0]), r6(cur[1] * f[1]), r6(cur[2] * f[2])}
}

// FormatSize renders extents as "W 150 × D 80 × H 40 mm".
func FormatSize(s [3]float64) string {
	return fmt.Sprintf("W %s × D %s × H %s mm", fmtMM(s[0]), fmtMM(s[1]), fmtMM(s[2]))
}

func fmtMM(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", v), "0"), ".")
}
