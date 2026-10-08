package features

import (
	"fmt"
	"math"

	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/selectors"
	"github.com/accelone/simoncad/internal/xform"
)

func init() {
	register(&Feature{Type: "scale", Build: buildScale, Target: targetOf})
	// A copy reads its source without consuming it: both stay visible bodies.
	register(&Feature{Type: "copy", Build: buildCopy, KeepsInputs: true})
	register(&Feature{Type: "face_extrude", Build: buildFaceExtrude, Target: targetOf})
	register(&Feature{Type: "face_inset", Build: buildFaceInset, Target: targetOf})
	register(&Feature{Type: "face_offset", Build: buildFaceOffset, Target: targetOf})
}

// ScaleAnchors are the accepted values of a scale feature's `about` parameter.
var ScaleAnchors = []string{"center", "base", "origin", "point"}

// ScaleFactors reads `factor` (uniform) or per-axis sx/sy/sz (each defaulting to factor or 1).
func ScaleFactors(p Params) ([3]float64, error) {
	f, err := p.NumOr("factor", 1)
	if err != nil {
		return [3]float64{}, err
	}
	var out [3]float64
	for i, k := range []string{"sx", "sy", "sz"} {
		v, err := p.NumOr(k, f)
		if err != nil {
			return out, err
		}
		if !(v > 0) || v > 1000 {
			return out, fmt.Errorf("scale factor %s=%g must be in (0, 1000]", k, v)
		}
		out[i] = v
	}
	if out == [3]float64{1, 1, 1} {
		return out, fmt.Errorf("scale factors are all 1 (nothing to scale)")
	}
	return out, nil
}

// ScaleAnchor is the fixed point of a scale: the body's bounding-box center (default), the
// center of its base (min Z, so a part on the print bed stays on it), the world origin, or (ox,oy,oz).
func ScaleAnchor(p Params, b selectors.Bounds) ([3]float64, error) {
	c := b.Center()
	switch p.Str("about") {
	case "", "center":
		return c, nil
	case "base":
		return [3]float64{c[0], c[1], b.Min[2]}, nil
	case "origin":
		return [3]float64{}, nil
	case "point":
		var o [3]float64
		for i, k := range []string{"ox", "oy", "oz"} {
			v, err := p.NumOr(k, 0)
			if err != nil {
				return o, err
			}
			o[i] = v
		}
		return o, nil
	}
	return [3]float64{}, fmt.Errorf("about must be center, base, origin or point (got %q)", p.Str("about"))
}

// ScaleFrame reads the optional local frame of a scale: the rotation (frame_ax, frame_ay,
// frame_az, frame_angle_deg) that maps the frame's axes onto the world. sx/sy/sz then act
// along the frame's axes, which is how a rotated object is resized along its own width,
// depth and height. No frame (or a zero angle) is the world frame.
func ScaleFrame(p Params) (xform.Rot, error) {
	deg, err := p.NumOr("frame_angle_deg", 0)
	if err != nil || deg == 0 {
		return xform.Identity, err
	}
	var axis [3]float64
	for i, k := range []string{"frame_ax", "frame_ay", "frame_az"} {
		if axis[i], err = p.NumOr(k, 0); err != nil {
			return xform.Identity, err
		}
	}
	if axis == [3]float64{} {
		return xform.Identity, fmt.Errorf("frame_angle_deg needs a non-zero frame axis (frame_ax, frame_ay, frame_az)")
	}
	return xform.AxisAngle(axis, deg), nil
}

// BoundsInFrame is the bounding box of the body measured along the axes of r (world = r·local):
// the box of the body rotated into that frame. The world frame is the ordinary bounding box.
func (b *Body) BoundsInFrame(r xform.Rot) (selectors.Bounds, error) {
	if r.IsIdentity(1e-12) {
		return b.Bounds(), nil
	}
	l, err := rotateShape(b.Shape, r.T())
	if err != nil {
		return selectors.Bounds{}, err
	}
	defer l.Release()
	return shapeBounds(l)
}

func rotateShape(s *api.Shape, r xform.Rot) (*api.Shape, error) {
	ax, deg := r.ToAxisAngle()
	return s.Rotate(0, 0, 0, ax[0], ax[1], ax[2], degToRad(deg))
}

func shapeBounds(s *api.Shape) (selectors.Bounds, error) {
	p, err := s.Props()
	if err != nil {
		return selectors.Bounds{}, err
	}
	return selectors.Bounds{Min: [3]float64{p.MinX, p.MinY, p.MinZ}, Max: [3]float64{p.MaxX, p.MaxY, p.MaxZ}}, nil
}

func buildScale(c *Ctx) (*api.Shape, error) {
	t, err := c.body(c.P.Str("target"))
	if err != nil {
		return nil, err
	}
	f, err := ScaleFactors(c.P)
	if err != nil {
		return nil, err
	}
	fr, err := ScaleFrame(c.P)
	if err != nil {
		return nil, err
	}
	if fr.IsIdentity(1e-12) {
		o, err := ScaleAnchor(c.P, t.Bounds())
		if err != nil {
			return nil, err
		}
		return t.Shape.Scale(o[0], o[1], o[2], f[0], f[1], f[2])
	}
	// In a local frame: rotate the body into the frame, scale along its axes there (the
	// anchor computed from the body's box in that frame) and rotate back.
	local, err := rotateShape(t.Shape, fr.T())
	if err != nil {
		return nil, err
	}
	defer local.Release()
	lb, err := shapeBounds(local)
	if err != nil {
		return nil, err
	}
	o, err := ScaleAnchor(c.P, lb)
	if err != nil {
		return nil, err
	}
	if a := c.P.Str("about"); a == "origin" || a == "point" { // world points: express them in the frame
		o = fr.T().Apply(o)
	}
	scaled, err := local.Scale(o[0], o[1], o[2], f[0], f[1], f[2])
	if err != nil {
		return nil, err
	}
	defer scaled.Release()
	return rotateShape(scaled, fr)
}

// buildCopy is a linked duplicate: the source body moved by (dx,dy,dz). It follows later
// parameter edits of the source.
func buildCopy(c *Ctx) (*api.Shape, error) {
	s, err := c.body(c.P.Str("source"))
	if err != nil {
		return nil, err
	}
	dx, _ := c.P.NumOr("dx", 0)
	dy, _ := c.P.NumOr("dy", 0)
	dz, _ := c.P.NumOr("dz", 0)
	return s.Shape.Translate(dx, dy, dz)
}

// targetFace resolves references.face, which must be a face of the feature's own target.
func targetFace(c *Ctx, planar bool) (selectors.Descriptor, *Body, error) {
	raw := c.Node.References["face"]
	if raw == "" {
		return selectors.Descriptor{}, nil, fmt.Errorf("references.face is required")
	}
	sel, err := selectors.Parse(raw)
	if err != nil {
		return selectors.Descriptor{}, nil, err
	}
	if sel.Kind != "face" {
		return selectors.Descriptor{}, nil, fmt.Errorf("%s is not a face selector", sel.String())
	}
	b, err := c.body(sel.Feature())
	if err != nil {
		return selectors.Descriptor{}, nil, fmt.Errorf("unresolved selector %s: %w", sel.String(), err)
	}
	d, err := selectors.ResolveTopo(sel, b.Topology())
	if err != nil {
		return d, nil, err
	}
	if d.FeatureID != c.P.Str("target") {
		return d, nil, fmt.Errorf("face %s is not on the target %q", d.Selector, c.P.Str("target"))
	}
	if planar && d.SurfaceType != "plane" {
		return d, nil, fmt.Errorf("face %s is %s; only planar faces can be extruded or inset", d.Selector, d.SurfaceType)
	}
	return d, b, nil
}

func signedDistance(p Params, name string) (float64, error) {
	v, err := p.Num(name)
	if err != nil {
		return 0, err
	}
	if math.Abs(v) < 1e-6 {
		return 0, fmt.Errorf("%s must be non-zero (positive adds material, negative removes it)", name)
	}
	return v, nil
}

// sweepFace sweeps a planar face along its outward normal by d and merges it with the body
// (d > 0) or removes it from the body (d < 0). Coplanar seams are merged, so pulling the top
// of a box leaves one clean side face per side (selectable as a whole by side=).
func sweepFace(c *Ctx, body *api.Shape, face *api.Shape, normal [3]float64, d float64) (*api.Shape, error) {
	pr, err := face.Prism(normal[0]*d, normal[1]*d, normal[2]*d)
	if err != nil {
		return nil, err
	}
	defer pr.Release()
	raw, err := applyTool(c.K, body, pr, d < 0)
	if err != nil {
		return nil, err
	}
	defer raw.Release()
	return raw.UnifyFaces()
}

// buildFaceExtrude pushes/pulls a planar face: positive distance adds a prism of the face's
// shape outward, negative cuts it into the body.
func buildFaceExtrude(c *Ctx) (*api.Shape, error) {
	d, b, err := targetFace(c, true)
	if err != nil {
		return nil, err
	}
	dist, err := signedDistance(c.P, "distance")
	if err != nil {
		return nil, err
	}
	f, err := b.Shape.Face(d.Index)
	if err != nil {
		return nil, err
	}
	defer f.Release()
	return sweepFace(c, b.Shape, f, *d.Normal, dist)
}

// buildFaceInset shrinks a planar face by `inset` from its edges. With a depth it raises
// (depth > 0) or sinks (depth < 0) the inset region; without one it imprints the inset
// outline, splitting the face so the inner face can be selected and edited on its own.
func buildFaceInset(c *Ctx) (*api.Shape, error) {
	d, b, err := targetFace(c, true)
	if err != nil {
		return nil, err
	}
	in, err := c.P.Positive("inset")
	if err != nil {
		return nil, err
	}
	depth, err := c.P.NumOr("depth", 0)
	if err != nil {
		return nil, err
	}
	if depth == 0 {
		return b.Shape.ImprintInset(d.Index, in)
	}
	f, err := b.Shape.FaceInset(d.Index, in)
	if err != nil {
		return nil, err
	}
	defer f.Release()
	return sweepFace(c, b.Shape, f, *d.Normal, depth)
}

// buildFaceOffset moves one face along its normal (any surface: a cylindrical face changes
// radius), extending or trimming the faces around it. For planar faces the kernel's offset is
// tried first and a face sweep is the fallback.
func buildFaceOffset(c *Ctx) (*api.Shape, error) {
	d, b, err := targetFace(c, false)
	if err != nil {
		return nil, err
	}
	dist, err := signedDistance(c.P, "distance")
	if err != nil {
		return nil, err
	}
	s, err := b.Shape.OffsetFace(d.Index, dist)
	if err == nil || d.Normal == nil {
		return s, err
	}
	f, ferr := b.Shape.Face(d.Index)
	if ferr != nil {
		return nil, err
	}
	defer f.Release()
	return sweepFace(c, b.Shape, f, *d.Normal, dist)
}
