package rendering

import (
	"math"

	"github.com/accelone/simoncad/internal/cadir"
	"github.com/accelone/simoncad/internal/geometry/curve"
	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/geometry/rebuild"
)

// PathPointView is one control point of a path as the editor draws it.
type PathPointView struct {
	P     [3]float64  `json:"p"`
	In    *[3]float64 `json:"in,omitempty"`
	Out   *[3]float64 `json:"out,omitempty"`
	W     float64     `json:"w,omitempty"`
	H     float64     `json:"h,omitempty"`
	Twist float64     `json:"twist,omitempty"`
}

// PathLines is a path node drawn as a polyline (xyz pairs, 6 floats per segment) plus the
// control data the interactive editor needs. Paths hold no geometry, so like sketches they are
// small and always sent whole.
type PathLines struct {
	ID     string          `json:"id"`
	Name   string          `json:"name,omitempty"`
	Type   string          `json:"type"`
	Closed bool            `json:"closed"`
	Length float64         `json:"length"`
	Lines  []float32       `json:"lines"`
	Points []PathPointView `json:"points"`
	// Users are the sweeps that run along this path (the editor previews them while dragging).
	Users []string `json:"users,omitempty"`
}

// PathsOf renders every healthy, visible path node of the model.
func PathsOf(g *cadir.Graph, m *rebuild.Model) []*PathLines {
	out := []*PathLines{}
	for _, n := range g.Nodes {
		if n.Type != features.PathType {
			continue
		}
		res := m.Nodes[n.ID]
		if res == nil || res.Status != cadir.StatusOK {
			continue
		}
		pa, err := features.PathFromParams(res.Params)
		if err != nil {
			continue
		}
		c, err := curve.New(pa)
		if err != nil {
			continue
		}
		// fine enough to look round at any zoom, coarse enough to stay light
		poly := c.Resample(math.Max(0.02, c.Length()/4000))
		pl := &PathLines{ID: n.ID, Name: n.Metadata.Name, Type: string(pa.Type), Closed: pa.Closed, Length: c.Length(), Lines: []float32{}}
		for i := 0; i+1 < len(poly); i++ {
			pl.Lines = append(pl.Lines, float32(poly[i][0]), float32(poly[i][1]), float32(poly[i][2]),
				float32(poly[i+1][0]), float32(poly[i+1][1]), float32(poly[i+1][2]))
		}
		if pa.Closed && len(poly) > 1 {
			a, b := poly[len(poly)-1], poly[0]
			pl.Lines = append(pl.Lines, float32(a[0]), float32(a[1]), float32(a[2]), float32(b[0]), float32(b[1]), float32(b[2]))
		}
		for _, pt := range pa.Points {
			v := PathPointView{P: pt.P, W: pt.W, H: pt.H, Twist: pt.Twist}
			if pt.In != nil {
				in := *pt.In
				v.In = &in
			}
			if pt.Out != nil {
				o := *pt.Out
				v.Out = &o
			}
			pl.Points = append(pl.Points, v)
		}
		for _, d := range g.Dependents(n.ID) {
			if dn := g.Get(d); dn != nil && dn.Type == features.SweepType {
				pl.Users = append(pl.Users, d)
			}
		}
		out = append(out, pl)
	}
	return out
}
