// Package profile defines reusable 2D cross-sections (contours with holes) that a sweep
// transports along a path. A profile is independent of any path and is plain data.
package profile

import (
	"fmt"
	"math"
	"sort"
)

// Vec2 is a 2D point in the profile plane (X, Y of the swept section).
type Vec2 = [2]float64

// Contour is one loop of a profile. Hole contours subtract from the outer contours.
type Contour struct {
	Points []Vec2
	Closed bool
	Hole   bool
}

// Profile is a set of contours. Type and Params record how it was made so builders can use
// an exact (analytic) section where the kernel supports it.
type Profile struct {
	ID       string
	Type     string
	Params   map[string]float64
	Contours []Contour
}

// Types lists the preset profile names.
func Types() []string {
	out := []string{"circle", "ellipse", "square", "rectangle", "triangle", "semicircle", "polygon", "hollow_circle", "custom"}
	sort.Strings(out)
	return out
}

const defaultSegments = 48

// Params accepts numbers by name; "vertices" is the one list parameter.
type Params struct {
	Num      map[string]float64
	Vertices []Vec2
	Strings  map[string]string
}

func (p Params) num(name string, def float64) float64 {
	if v, ok := p.Num[name]; ok {
		return v
	}
	return def
}

func (p Params) positive(name string, def float64) (float64, error) {
	v := p.num(name, def)
	if !(v > 0) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s must be a positive number, got %v", name, v)
	}
	return v, nil
}

func segments(p Params) (int, error) {
	n := int(p.num("segments", defaultSegments))
	if n < 3 || n > 4096 {
		return 0, fmt.Errorf("segments must be between 3 and 4096, got %d", n)
	}
	return n, nil
}

func ring(rx, ry float64, n int, a0, a1 float64, full bool) []Vec2 {
	pts := make([]Vec2, 0, n+1)
	steps := n
	if !full {
		steps = n
	}
	for i := 0; i <= steps; i++ {
		if full && i == steps {
			break
		}
		a := a0 + (a1-a0)*float64(i)/float64(steps)
		pts = append(pts, Vec2{rx * math.Cos(a), ry * math.Sin(a)})
	}
	return pts
}

// New builds a preset profile. All presets are centred on the origin and wound
// counter-clockwise (holes clockwise).
func New(typeName string, p Params) (*Profile, error) {
	pr := &Profile{Type: typeName, Params: map[string]float64{}}
	for k, v := range p.Num {
		pr.Params[k] = v
	}
	switch typeName {
	case "circle":
		r, err := p.positive("radius", 0)
		if err != nil {
			return nil, err
		}
		n, err := segments(p)
		if err != nil {
			return nil, err
		}
		pr.Contours = []Contour{{Points: ring(r, r, n, 0, 2*math.Pi, true), Closed: true}}
	case "ellipse":
		rx, err := p.positive("radius_x", 0)
		if err != nil {
			return nil, err
		}
		ry, err := p.positive("radius_y", 0)
		if err != nil {
			return nil, err
		}
		n, err := segments(p)
		if err != nil {
			return nil, err
		}
		pr.Contours = []Contour{{Points: ring(rx, ry, n, 0, 2*math.Pi, true), Closed: true}}
	case "square":
		s, err := p.positive("side", 0)
		if err != nil {
			return nil, err
		}
		pr.Contours = []Contour{rect(s, s)}
	case "rectangle":
		w, err := p.positive("width", 0)
		if err != nil {
			return nil, err
		}
		h, err := p.positive("height", 0)
		if err != nil {
			return nil, err
		}
		pr.Contours = []Contour{rect(w, h)}
	case "triangle":
		w, err := p.positive("width", 0)
		if err != nil {
			return nil, err
		}
		h, err := p.positive("height", 0)
		if err != nil {
			return nil, err
		}
		pr.Contours = []Contour{{Points: []Vec2{{-w / 2, -h / 2}, {w / 2, -h / 2}, {0, h / 2}}, Closed: true}}
	case "semicircle":
		r, err := p.positive("radius", 0)
		if err != nil {
			return nil, err
		}
		n, err := segments(p)
		if err != nil {
			return nil, err
		}
		// Flat side on the profile's local -Y by default; orientation is the rotation of the
		// round side in degrees (0 = round side up / +Y, 90 = +X pointing left ... CCW).
		pts := ring(r, r, n, 0, math.Pi, false) // arc from +X over +Y to -X, flat chord closes it
		pr.Contours = []Contour{{Points: pts, Closed: true}}
		if o := p.num("orientation", 0); o != 0 {
			pr.Contours[0] = rotateContour(pr.Contours[0], o*math.Pi/180)
		}
	case "polygon":
		if len(p.Vertices) < 3 {
			return nil, fmt.Errorf("polygon needs at least 3 vertices, got %d", len(p.Vertices))
		}
		pr.Contours = []Contour{{Points: append([]Vec2(nil), p.Vertices...), Closed: true}}
	case "hollow_circle":
		ro, err := p.positive("outer_radius", 0)
		if err != nil {
			return nil, err
		}
		ri, err := p.positive("inner_radius", 0)
		if err != nil {
			return nil, err
		}
		if ri >= ro {
			return nil, fmt.Errorf("inner_radius (%v) must be smaller than outer_radius (%v)", ri, ro)
		}
		n, err := segments(p)
		if err != nil {
			return nil, err
		}
		pr.Contours = []Contour{
			{Points: ring(ro, ro, n, 0, 2*math.Pi, true), Closed: true},
			{Points: ring(ri, ri, n, 0, 2*math.Pi, true), Closed: true, Hole: true},
		}
	default:
		return nil, fmt.Errorf("unknown profile type %q (use one of %v, or create a custom profile from contours)", typeName, Types())
	}
	pr.Normalize()
	return pr, nil
}

func rect(w, h float64) Contour {
	return Contour{Points: []Vec2{{-w / 2, -h / 2}, {w / 2, -h / 2}, {w / 2, h / 2}, {-w / 2, h / 2}}, Closed: true}
}

// Custom builds a profile from arbitrary contours.
func Custom(contours []Contour) (*Profile, error) {
	pr := &Profile{Type: "custom", Contours: make([]Contour, len(contours))}
	for i, c := range contours {
		pr.Contours[i] = Contour{Points: append([]Vec2(nil), c.Points...), Closed: c.Closed, Hole: c.Hole}
	}
	pr.Normalize()
	if issues := pr.Validate(); HasError(issues) {
		return nil, fmt.Errorf("invalid custom profile: %s", issues[firstError(issues)].Message)
	}
	return pr, nil
}

// SignedArea is positive for counter-clockwise loops.
func SignedArea(pts []Vec2) float64 {
	a := 0.0
	for i := range pts {
		j := (i + 1) % len(pts)
		a += pts[i][0]*pts[j][1] - pts[j][0]*pts[i][1]
	}
	return a / 2
}

// Normalize fixes orientation: outer contours counter-clockwise, holes clockwise.
func (p *Profile) Normalize() {
	for i := range p.Contours {
		c := &p.Contours[i]
		if !c.Closed {
			continue
		}
		a := SignedArea(c.Points)
		if (!c.Hole && a < 0) || (c.Hole && a > 0) {
			for l, r := 0, len(c.Points)-1; l < r; l, r = l+1, r-1 {
				c.Points[l], c.Points[r] = c.Points[r], c.Points[l]
			}
		}
	}
}

// Clone deep-copies the profile.
func (p *Profile) Clone() *Profile {
	q := &Profile{ID: p.ID, Type: p.Type, Contours: make([]Contour, len(p.Contours))}
	if p.Params != nil {
		q.Params = map[string]float64{}
		for k, v := range p.Params {
			q.Params[k] = v
		}
	}
	for i, c := range p.Contours {
		q.Contours[i] = Contour{Points: append([]Vec2(nil), c.Points...), Closed: c.Closed, Hole: c.Hole}
	}
	return q
}

// Scaled returns a copy scaled by sx, sy about the origin. A mirroring scale (negative) is
// rejected: it would flip orientation, so use Normalize after if you really want it.
func (p *Profile) Scaled(sx, sy float64) (*Profile, error) {
	if !(sx > 0) || !(sy > 0) {
		return nil, fmt.Errorf("profile scale factors must be positive, got %v, %v", sx, sy)
	}
	q := p.Clone()
	for i := range q.Contours {
		for j, v := range q.Contours[i].Points {
			q.Contours[i].Points[j] = Vec2{v[0] * sx, v[1] * sy}
		}
	}
	q.Params = nil // preset parameters no longer describe the shape
	return q, nil
}

func rotateContour(c Contour, a float64) Contour {
	cs, sn := math.Cos(a), math.Sin(a)
	out := Contour{Points: make([]Vec2, len(c.Points)), Closed: c.Closed, Hole: c.Hole}
	for i, v := range c.Points {
		out.Points[i] = Vec2{v[0]*cs - v[1]*sn, v[0]*sn + v[1]*cs}
	}
	return out
}

// Rotated returns a copy rotated by angle degrees counter-clockwise about the origin.
func (p *Profile) Rotated(angleDeg float64) *Profile {
	q := p.Clone()
	a := angleDeg * math.Pi / 180
	for i := range q.Contours {
		q.Contours[i] = rotateContour(q.Contours[i], a)
	}
	q.Params = nil
	return q
}

// Bounds returns min and max corners over every contour.
func (p *Profile) Bounds() (lo, hi Vec2) {
	lo = Vec2{math.Inf(1), math.Inf(1)}
	hi = Vec2{math.Inf(-1), math.Inf(-1)}
	for _, c := range p.Contours {
		for _, v := range c.Points {
			lo = Vec2{math.Min(lo[0], v[0]), math.Min(lo[1], v[1])}
			hi = Vec2{math.Max(hi[0], v[0]), math.Max(hi[1], v[1])}
		}
	}
	return
}

// Area is the net area (outer contours minus holes).
func (p *Profile) Area() float64 {
	a := 0.0
	for _, c := range p.Contours {
		if !c.Closed {
			continue
		}
		v := math.Abs(SignedArea(c.Points))
		if c.Hole {
			a -= v
		} else {
			a += v
		}
	}
	return a
}

// MaxRadius is the largest distance from the origin to any profile vertex: the half-size a
// sweep must stay clear of the path's curvature radius.
func (p *Profile) MaxRadius() float64 {
	m := 0.0
	for _, c := range p.Contours {
		for _, v := range c.Points {
			m = math.Max(m, math.Hypot(v[0], v[1]))
		}
	}
	return m
}
