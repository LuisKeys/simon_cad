package features

import (
	"fmt"
	"math"

	"github.com/accelone/simoncad/internal/geometry/api"
	"github.com/accelone/simoncad/internal/geometry/sweep"
)

// SweepType is the node type of a profile swept along a path. Its dependencies are the path and
// profile nodes; the node is the solid, so a path edit regenerates it through the rebuild cache.
const SweepType = "sweep"

func init() {
	register(&Feature{Type: SweepType, Build: buildSweep, KeepsInputs: true})
}

// SweepOptions reads the sweep node's own parameters.
func sweepOptions(p Params) (sweep.Options, error) {
	o := sweep.Options{CapStart: p.Str("cap_start"), CapEnd: p.Str("cap_end")}
	var err error
	if o.Tolerance, err = p.NumOr("tolerance", 0); err != nil {
		return o, err
	}
	if o.CapLength, err = p.NumOr("cap_length", 0); err != nil {
		return o, err
	}
	if o.TotalTwist, err = p.NumOr("twist", 0); err != nil {
		return o, err
	}
	ms, err := p.NumOr("max_sections", 0)
	if err != nil {
		return o, err
	}
	o.MaxSections = int(ms)
	if v, ok := p["up"]; ok {
		nv, err := normalize(v)
		if err != nil {
			return o, err
		}
		up, err := vec3(nv)
		if err != nil {
			return o, fmt.Errorf("up: %w", err)
		}
		o.Up = up
	}
	return o, nil
}

// codedFromSweep converts a planner error into the machine-readable error of this package.
func codedFromSweep(err error) error {
	if e, ok := sweep.AsError(err); ok {
		return &CodedError{Code: e.Code, Msg: e.Msg, Hint: e.Hint}
	}
	return err
}

// PlanSweep runs the planner for a sweep node given the resolved path and profile parameters.
// Tools call it to validate a sweep before it is added and to report warnings.
func PlanSweep(pathP, profileP, sweepP Params) (*sweep.Plan, error) {
	pa, err := PathFromParams(pathP)
	if err != nil {
		return nil, err
	}
	pr, err := ProfileFromParams(profileP)
	if err != nil {
		return nil, err
	}
	opt, err := sweepOptions(sweepP)
	if err != nil {
		return nil, Coded(CodeSweepFailed, "", "%v", err)
	}
	plan, err := sweep.Build(pa, pr, opt)
	if err != nil {
		return nil, codedFromSweep(err)
	}
	return plan, nil
}

func spec(j sweep.Job) api.SweepSpec {
	sp := api.SweepSpec{Closed: j.Closed, Ruled: j.Ruled, ApexStart: j.ApexStart, ApexEnd: j.ApexEnd,
		CapStartN: j.CapStartN, CapEndN: j.CapEndN, CapStartRuled: j.CapStartRuled, CapEndRuled: j.CapEndRuled}
	for _, s := range j.Sections {
		sp.Sections = append(sp.Sections, api.SweepSection{Origin: s.Origin, X: s.X, Y: s.Y, SX: s.SX, SY: s.SY})
	}
	for _, c := range j.Contours {
		sp.Contours = append(sp.Contours, api.SweepContour{Ellipse: c.Ellipse, Hole: c.Hole, RX: c.RX, RY: c.RY,
			Points: c.Points, PerSection: c.PerSection})
	}
	return sp
}

func buildSweep(c *Ctx) (*api.Shape, error) {
	pid, prid := c.P.Str("path"), c.P.Str("profile")
	pathP, ok := c.ParamsOf(pid)
	if !ok {
		return nil, Coded(CodePathInvalid, "pass path: the id of a path node", "sweep has no path %q", pid)
	}
	profP, ok := c.ParamsOf(prid)
	if !ok {
		return nil, Coded(CodeProfileInvalid, "pass profile: the id of a profile node", "sweep has no profile %q", prid)
	}
	plan, err := PlanSweep(pathP, profP, c.P)
	if err != nil {
		return nil, err
	}
	body, err := c.K.Sweep(spec(plan.Body))
	if err != nil {
		return nil, Coded(CodeSweepFailed, "check the path and profile sizes; a smaller profile or gentler bends usually fix it", "%v", err)
	}
	// A loft through nearly singular geometry can come back "valid" yet wrong; the analytic
	// volume of a sweep is known, so compare.
	if want := plan.ExpectedVolume; want > 0 {
		props, err := body.Props()
		if err != nil {
			body.Release()
			return nil, err
		}
		if math.Abs(props.Volume-want)/want > sweepVolumeTolerance {
			body.Release()
			return nil, Coded(CodeSweepFailed,
				"use a thinner profile, a gentler path (larger bend radius), or fewer sections (max_sections)",
				"the kernel produced an unstable sweep: volume %.4g mm³ where %.4g mm³ is expected", props.Volume, want)
		}
	}
	return body, nil
}

// sweepVolumeTolerance is the relative gap between the kernel's volume and the analytic one
// that marks a sweep as unreliable (smooth lofts differ from the analytic value by <1%).
const sweepVolumeTolerance = 0.10
