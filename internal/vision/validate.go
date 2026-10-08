package vision

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Missing is a value only the user can supply. Ask is a short hint for the question.
type Missing struct {
	Dimension string `json:"dimension"`
	Kind      string `json:"kind"`
	Ask       string `json:"ask"`
}

// Ambiguity is a detected situation the drawing does not settle.
type Ambiguity struct {
	Profiles []string `json:"profiles"`
	Question string   `json:"question"`
}

// Report is the deterministic verdict on a proposal. Ready means create_from_image may run.
type Report struct {
	Ready       bool        `json:"ready"`
	Errors      []string    `json:"errors,omitempty"`
	Warnings    []string    `json:"warnings,omitempty"`
	Missing     []Missing   `json:"missing,omitempty"`
	Unconfirmed []string    `json:"unconfirmed,omitempty"` // dimensions read with low confidence
	Ambiguous   []Ambiguity `json:"ambiguous,omitempty"`
	Scale       *Scale      `json:"scale,omitempty"`
}

// Validate checks structure, closure, containment, operation order and dimensions.
// gapTol is the largest endpoint gap (px) that is snapped shut; <=0 uses 1.5% of the image diagonal.
func Validate(p *Proposal, explicit *Scale) *Report {
	r := &Report{}
	errf := func(f string, a ...any) { r.Errors = append(r.Errors, fmt.Sprintf(f, a...)) }

	diag := math.Hypot(p.ImageSize[0], p.ImageSize[1])
	if !(p.ImageSize[0] > 0 && p.ImageSize[1] > 0) {
		errf("image_size must be [width,height] in pixels")
		diag = 1000
	}
	gapTol := 0.015 * diag

	// entities
	seen := map[string]bool{}
	for i, e := range p.Entities {
		id := e.ID()
		switch {
		case id == "" || !idPattern.MatchString(id):
			errf("entities[%d] needs an id of letters, digits and underscores", i)
			continue
		case seen[id]:
			errf("duplicate entity id %q", id)
			continue
		}
		seen[id] = true
		fields, ok := entityFields[e.Kind()]
		if !ok {
			errf("entity %q: kind must be line, arc, circle, rectangle, polygon or slot (got %q)", id, e.Kind())
			continue
		}
		for _, f := range fields {
			if _, has := e[f]; !has {
				errf("entity %q (%s) is missing %q", id, e.Kind(), f)
			}
		}
		if _, ok := closedPoly(e); !ok {
			if _, ok := openPts(e); !ok {
				errf("entity %q (%s) has invalid geometry", id, e.Kind())
			}
		}
		if c := e.Confidence(); c < 0 || c > 1 {
			errf("entity %q confidence must be within 0..1", id)
		}
	}
	if len(p.Entities) == 0 {
		errf("the proposal has no entities")
	}

	// profiles
	pseen := map[string]bool{}
	polys := map[string][]pt{}
	for _, pr := range p.Profiles {
		if !idPattern.MatchString(pr.ID) || pseen[pr.ID] {
			errf("profile id %q is invalid or duplicated", pr.ID)
			continue
		}
		pseen[pr.ID] = true
		outer, gap, err := loopOutline(p, pr.Outer)
		if err != nil {
			errf("profile %q outer loop: %v", pr.ID, err)
			continue
		}
		if gap > gapTol {
			errf("profile %q outer loop is not closed (gap %.1f px)", pr.ID, gap)
			continue
		}
		if selfIntersects(outer) {
			errf("profile %q outer loop crosses itself", pr.ID)
			continue
		}
		polys[pr.ID] = outer
		for hi, h := range pr.Holes {
			hp, gap, err := loopOutline(p, h)
			if err != nil {
				errf("profile %q hole %d: %v", pr.ID, hi, err)
				continue
			}
			if gap > gapTol {
				errf("profile %q hole %d is not closed (gap %.1f px)", pr.ID, hi, gap)
				continue
			}
			if selfIntersects(hp) {
				errf("profile %q hole %d crosses itself", pr.ID, hi)
				continue
			}
			inside := true
			for _, q := range hp {
				inside = inside && pointInPoly(q, outer)
			}
			if !inside || polysCross(hp, outer) {
				errf("profile %q hole %d is not inside its outer loop", pr.ID, hi)
			}
		}
	}
	if len(p.Profiles) == 0 {
		errf("the proposal has no profiles (closed regions); group entities into profiles")
	}

	// relationships
	for i, rel := range p.Relationships {
		if !relKinds[rel.Kind] {
			errf("relationships[%d]: unknown kind %q", i, rel.Kind)
		}
		for _, id := range rel.Entities {
			if p.entity(id) == nil {
				errf("relationships[%d]: unknown entity %q", i, id)
			}
		}
	}

	// dimensions
	dseen := map[string]bool{}
	for _, d := range p.Dimensions {
		switch {
		case !idPattern.MatchString(d.ID) || dseen[d.ID]:
			errf("dimension id %q is invalid or duplicated", d.ID)
			continue
		case !dimKinds[d.Kind]:
			errf("dimension %q: kind must be length, diameter, radius, depth or distance", d.ID)
		case !dimSources[d.Source]:
			errf("dimension %q: source must be handwritten, printed, inferred, missing, confirmed or user", d.ID)
		case d.Value != nil && !(*d.Value > 0):
			errf("dimension %q: value must be > 0", d.ID)
		case d.Confidence < 0 || d.Confidence > 1:
			errf("dimension %q: confidence must be within 0..1", d.ID)
		}
		dseen[d.ID] = true
		if d.Binds != "" {
			id, field, ok := d.bindParts()
			e := p.entity(id)
			switch {
			case !ok:
				errf("dimension %q: binds must look like entity.field (e1.w)", d.ID)
			case e == nil:
				errf("dimension %q binds unknown entity %q", d.ID, id)
			default:
				if _, known := bindFields[e.Kind()][field]; !known {
					errf("dimension %q cannot bind %s.%s (bindable: %s)", d.ID, e.Kind(), field, bindable(e.Kind()))
				}
			}
		}
	}

	r.opChecks(p, errf)

	// scale and what is still missing
	sc, warns := ResolveScale(p, explicit)
	r.Scale = sc
	r.Warnings = append(r.Warnings, warns...)
	var scaleAsk *Missing
	for i := range p.Dimensions {
		d := &p.Dimensions[i]
		switch {
		case d.Value == nil || d.Source == "missing":
			if d.Kind == "depth" {
				r.Missing = appendMissing(r.Missing, Missing{Dimension: d.ID, Kind: d.Kind, Ask: askFor(d)})
			} else if scaleAsk == nil && (d.Binds != "" || d.MeasuredPx != nil) {
				scaleAsk = &Missing{Dimension: d.ID, Kind: d.Kind, Ask: askFor(d)}
			}
		case !d.trusted():
			r.Unconfirmed = append(r.Unconfirmed, d.ID)
		}
	}
	// One real linear dimension fixes the scale for the whole drawing: ask for exactly one.
	if sc == nil && len(r.Errors) == 0 {
		if scaleAsk == nil {
			scaleAsk = &Missing{Dimension: "scale", Kind: "length", Ask: scaleQuestion(p)}
		}
		r.Missing = appendMissing(r.Missing, *scaleAsk)
	}
	// depths that operations need but no dimension supplies
	for _, op := range p.Operations {
		if (op.Kind == "extrude" || op.Kind == "cut") && !op.Through && op.DepthDim == "" {
			r.Missing = appendMissing(r.Missing, Missing{Dimension: "extrusion_depth", Kind: "depth", Ask: "extrusion depth (thickness)"})
		}
	}
	if len(p.Operations) == 0 {
		if DefaultDepth(p) == nil {
			r.Missing = appendMissing(r.Missing, Missing{Dimension: "extrusion_depth", Kind: "depth", Ask: "extrusion depth (thickness)"})
		}
		r.Ambiguous = overlaps(p, polys)
	}
	sort.Strings(r.Unconfirmed)
	r.Ready = len(r.Errors) == 0 && len(r.Missing) == 0 && len(r.Unconfirmed) == 0 && len(r.Ambiguous) == 0 && sc != nil
	return r
}

func bindable(kind string) string {
	var out []string
	for f := range bindFields[kind] {
		out = append(out, f)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

func appendMissing(list []Missing, m Missing) []Missing {
	for _, x := range list {
		if x.Dimension == m.Dimension {
			return list
		}
	}
	return append(list, m)
}

func askFor(d *Dimension) string {
	if d.Label != "" {
		return d.Label
	}
	if d.Binds != "" {
		return "the " + d.Kind + " of " + d.Binds
	}
	return "the " + d.Kind + " " + d.ID
}

// scaleQuestion names the single most useful dimension to ask for: the widest profile's overall width.
func scaleQuestion(p *Proposal) string {
	best, bestW, bestID := "", 0.0, ""
	for _, pr := range p.Profiles {
		poly, _, err := loopOutline(p, pr.Outer)
		if err != nil {
			continue
		}
		mn, mx := bboxOf(poly)
		if w := mx[0] - mn[0]; w > bestW {
			best, bestW, bestID = pr.ID, w, pr.ID
		}
	}
	if best == "" {
		return "any one real dimension of the drawing"
	}
	return "the overall width of profile " + bestID + " (or any one real dimension of the drawing)"
}

// opChecks validates operation ids, references and ordering.
func (r *Report) opChecks(p *Proposal, errf func(string, ...any)) {
	done := map[string]bool{}
	used := map[string]bool{}
	ref := func(opID, what, v string) {
		switch {
		case v == "":
			errf("operation %q needs %s", opID, what)
		case v == "selection" || strings.HasPrefix(v, "feature:"):
		case !done[v]:
			if p.profile(v) != nil {
				errf("operation %q: %s %q is a profile; extrude it with an earlier extrude operation first", opID, what, v)
			} else {
				errf("operation %q: %s %q is not an earlier operation", opID, what, v)
			}
		default:
			if used[v] {
				errf("operation %q: %q was already consumed by another operation", opID, v)
			}
			used[v] = true
		}
	}
	for _, op := range p.Operations {
		if !idPattern.MatchString(op.ID) || done[op.ID] {
			errf("operation id %q is invalid or duplicated", op.ID)
			continue
		}
		if !opKinds[op.Kind] {
			errf("operation %q: kind must be extrude, cut, union, subtract or intersect", op.ID)
			continue
		}
		switch op.Kind {
		case "extrude", "cut":
			if op.Profile != "" && p.profile(op.Profile) == nil {
				errf("operation %q: unknown profile %q", op.ID, op.Profile)
			}
			if op.Kind == "cut" {
				if op.Profile == "" {
					errf("cut %q needs a profile", op.ID)
				}
				ref(op.ID, "target", op.Target)
				if op.Through && op.DepthDim != "" {
					errf("cut %q: give depth_dim or through, not both", op.ID)
				}
			}
			if op.DepthDim != "" {
				if d := p.dimension(op.DepthDim); d == nil || d.Kind != "depth" {
					errf("operation %q: depth_dim %q must name a depth dimension", op.ID, op.DepthDim)
				}
			}
		default:
			ref(op.ID, "a", op.A)
			ref(op.ID, "b", op.B)
			if op.A == op.B && op.A != "" {
				errf("operation %q combines %q with itself", op.ID, op.A)
			}
		}
		done[op.ID] = true
	}
	if pl := p.Placement; !planeAllowed[pl.Plane] {
		errf("placement.plane must be XY, XZ or YZ")
	}
}

// overlaps finds profile pairs whose outlines cross or nest without being declared a hole:
// the drawing then does not say whether to join, subtract, intersect or keep them apart.
func overlaps(p *Proposal, polys map[string][]pt) []Ambiguity {
	holeOf := map[string]bool{}
	for _, pr := range p.Profiles {
		for _, h := range pr.Holes {
			for _, id := range h {
				holeOf[id] = true
			}
		}
	}
	var ids []string
	for _, pr := range p.Profiles {
		if _, ok := polys[pr.ID]; ok {
			ids = append(ids, pr.ID)
		}
	}
	var out []Ambiguity
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, b := polys[ids[i]], polys[ids[j]]
			if polysCross(a, b) || pointInPoly(a[0], b) || pointInPoly(b[0], a) {
				out = append(out, Ambiguity{Profiles: []string{ids[i], ids[j]},
					Question: fmt.Sprintf("Profiles %s and %s overlap. Join them, subtract one from the other, keep only the intersection, or keep them separate?", ids[i], ids[j])})
			}
		}
	}
	return out
}

// DefaultDepth is the depth dimension a proposal without operations extrudes by: the first one declared.
func DefaultDepth(p *Proposal) *Dimension {
	for i := range p.Dimensions {
		if p.Dimensions[i].Kind == "depth" {
			return &p.Dimensions[i]
		}
	}
	return nil
}
