package visual

import (
	"math"
	"sort"
	"strings"
)

// Landmarks are named points in normalized image coordinates (x right, y down, 0..1).
// Scalars (e.g. wheel_radius, as a fraction of the image width) use the x component.
type Landmarks map[string][2]float64

// VehicleLandmarks are the landmarks requested for vehicle-like objects.
var VehicleLandmarks = []string{
	"front_tip", "rear_tip", "front_wheel_center", "rear_wheel_center", "wheel_radius",
	"hood_start", "hood_end", "windshield_lower", "windshield_upper", "roof_front", "roof_rear",
	"belt_line", "body_bottom", "roof_max", "ground",
}

// FrontLandmarks are read from a front view (widths).
var FrontLandmarks = []string{"body_left", "body_right", "roof_left", "roof_right"}

// Ratios derives normalized proportions from side-view landmarks (and front-view ones when
// given). Pixel aspect matters: x is scaled by the image width and y by its height.
// Missing landmarks simply leave their ratios out.
func Ratios(side Landmarks, sideW, sideH int, front Landmarks) map[string]float64 {
	out := map[string]float64{}
	if side != nil && sideW > 0 && sideH > 0 {
		px := func(name string) ([2]float64, bool) {
			p, ok := side[name]
			return [2]float64{p[0] * float64(sideW), p[1] * float64(sideH)}, ok
		}
		ft, ok1 := px("front_tip")
		rt, ok2 := px("rear_tip")
		length := math.Abs(ft[0] - rt[0])
		if ok1 && ok2 && length > 0 {
			bottom, okB := px("body_bottom")
			if g, ok := px("ground"); ok {
				bottom, okB = g, true
			}
			top, okT := px("roof_max")
			height := 0.0
			if okB && okT {
				height = math.Abs(bottom[1] - top[1])
				out["height_length_ratio"] = height / length
			}
			fw, okF := px("front_wheel_center")
			rw, okR := px("rear_wheel_center")
			if okF && okR {
				out["wheelbase_ratio"] = math.Abs(fw[0]-rw[0]) / length
				out["front_overhang_ratio"] = math.Abs(ft[0]-fw[0]) / length
				out["rear_overhang_ratio"] = math.Abs(rt[0]-rw[0]) / length
			}
			rf, okRF := px("roof_front")
			rr, okRR := px("roof_rear")
			if wl, ok := px("windshield_lower"); ok && okRR {
				out["cabin_length_ratio"] = math.Abs(wl[0]-rr[0]) / length
			} else if okRF && okRR {
				out["cabin_length_ratio"] = math.Abs(rf[0]-rr[0]) / length
			}
			if r, ok := side["wheel_radius"]; ok && height > 0 {
				out["wheel_diameter_height_ratio"] = 2 * r[0] * float64(sideW) / height
			}
			wl, okL := px("windshield_lower")
			wu, okU := px("windshield_upper")
			if okL && okU && height > 0 {
				out["windshield_height_ratio"] = math.Abs(wl[1]-wu[1]) / height
			}
			if hs, ok := px("hood_start"); ok {
				if he, ok := px("hood_end"); ok {
					out["hood_length_ratio"] = math.Abs(hs[0]-he[0]) / length
				}
			}
		}
	}
	if front != nil {
		bl, ok1 := front["body_left"]
		br, ok2 := front["body_right"]
		rl, ok3 := front["roof_left"]
		rr, ok4 := front["roof_right"]
		if ok1 && ok2 && ok3 && ok4 && math.Abs(bl[0]-br[0]) > 0 {
			out["roof_width_ratio"] = math.Abs(rl[0]-rr[0]) / math.Abs(bl[0]-br[0])
		}
	}
	for k, v := range out {
		out[k] = round3(v)
	}
	return out
}

// CompareRatios scores how close the model's ratios are to the reference's (1 = equal).
// Each ratio scores 1 - relative error (clamped); the result is their mean, and 0 when no
// ratio is shared.
func CompareRatios(ref, model map[string]float64) (float64, map[string]float64) {
	per := map[string]float64{}
	sum, n := 0.0, 0
	for k, r := range ref {
		m, ok := model[k]
		if !ok || r <= 0 {
			continue
		}
		s := 1 - math.Min(1, math.Abs(m-r)/r)
		per[k] = round3(s)
		sum += s
		n++
	}
	if n == 0 {
		return 0, per
	}
	return round3(sum / float64(n)), per
}

// LandmarkAlignment compares landmarks of the same view after mapping each set into its
// own silhouette bounding box (so position and scale do not matter). mirrored flips the
// reference horizontally. Returns 1 - mean distance / 0.1 (clamped), over shared points;
// filter limits the comparison to names containing one of the given substrings.
func LandmarkAlignment(ref Landmarks, refBox [4]float64, model Landmarks, modelBox [4]float64, mirrored bool, filter ...string) (float64, bool) {
	sum, n := 0.0, 0
	names := make([]string, 0, len(ref))
	for k := range ref {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if k == "wheel_radius" || !matches(k, filter) {
			continue
		}
		m, ok := model[k]
		if !ok {
			continue
		}
		r := inBox(ref[k], refBox)
		if mirrored {
			r[0] = 1 - r[0]
		}
		g := inBox(m, modelBox)
		sum += math.Hypot(r[0]-g[0], r[1]-g[1])
		n++
	}
	if n == 0 {
		return 0, false
	}
	return round3(math.Max(0, 1-sum/float64(n)/0.1)), true
}

func matches(name string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if strings.Contains(name, f) {
			return true
		}
	}
	return false
}

// inBox maps a normalized image point into a normalized box (x0, y0, x1, y1 in 0..1).
func inBox(p [2]float64, b [4]float64) [2]float64 {
	w, h := b[2]-b[0], b[3]-b[1]
	if w <= 0 || h <= 0 {
		return p
	}
	return [2]float64{(p[0] - b[0]) / w, (p[1] - b[1]) / h}
}

// NormBox is a mask's bounding box in normalized image coordinates.
func NormBox(m *Mask) [4]float64 {
	x0, y0, x1, y1, ok := m.BBox()
	if !ok {
		return [4]float64{0, 0, 1, 1}
	}
	return [4]float64{float64(x0) / float64(m.W), float64(y0) / float64(m.H), float64(x1) / float64(m.W), float64(y1) / float64(m.H)}
}
