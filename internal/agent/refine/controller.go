package refine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/tools"
	"github.com/accelone/simoncad/internal/visual"
)

// Controller states, reported through OnEvent.
const (
	StateAnalyzing  = "ANALYZING"
	StatePlanning   = "PLANNING"
	StateBuilding   = "BUILDING"
	StateRendering  = "RENDERING"
	StateCritiquing = "CRITIQUING"
	StateCorrecting = "CORRECTING"
	StateReverting  = "REVERTING"
	StateAdvancing  = "ADVANCING"
	StateCompleted  = "COMPLETED"
	StateStopped    = "STOPPED"
	StateFailed     = "FAILED"
)

// Event is one progress report of a refinement run.
type Event struct {
	State     string             `json:"state"`
	Message   string             `json:"message"`
	Iteration int                `json:"iteration,omitempty"`
	Stage     string             `json:"stage,omitempty"`
	Scores    map[string]float64 `json:"scores,omitempty"`
}

// Defaults.
const (
	DefaultMaxIterations = 12
	DefaultMaxChanges    = 3
	DefaultTolerance     = 0.01
	DefaultRenderSize    = 768
)

// Controller is the RefinementController.
type Controller struct {
	App      *app.App
	Analyzer *Analyzer
	Planner  *Planner
	Builder  *Builder
	Critic   *VisionCritic

	MaxIterations int     // refinement iterations per run
	MaxChanges    int     // structural corrections per iteration (1..3)
	Tolerance     float64 // a correction is reverted when the overall score drops by more
	RenderSize    int

	OnEvent     func(Event)
	OnIteration func(*visual.Iteration)
	OnRevert    func(ids []string)

	strategy *Strategy
}

// Evaluation is one observation and measurement of the model.
type Evaluation struct {
	Renders     map[string]*visual.Render
	Silhouettes map[string]visual.SilhouetteScore
	Scores      map[string]float64
	Gate        visual.Gate
	Critique    *Critique
	Raw         json.RawMessage
}

// Overall is the weighted overall score.
func (e *Evaluation) Overall() float64 { return e.Scores[visual.MetricOverall] }

// Result summarizes a run.
type Result struct {
	State      string `json:"state"`
	Summary    string `json:"summary"`
	Iterations int    `json:"iterations"`
	Stage      string `json:"stage"`
	Final      map[string]float64
	// Usage is what the whole run consumed across the analyzer, planner, builder and critic.
	Usage runtime.Usage `json:"usage"`
}

// Usage totals what the run's four model roles have consumed so far.
func (c *Controller) Usage() runtime.Usage {
	var u runtime.Usage
	if c.Analyzer != nil {
		u.Add(c.Analyzer.Usage)
	}
	if c.Planner != nil {
		u.Add(c.Planner.Usage)
	}
	if c.Builder != nil {
		u.Add(c.Builder.Usage)
	}
	if c.Critic != nil {
		u.Add(c.Critic.Usage)
	}
	return u
}

func (c *Controller) emit(ev Event) {
	if c.OnEvent != nil {
		c.OnEvent(ev)
	}
}

func (c *Controller) defaults() {
	if c.MaxIterations <= 0 {
		c.MaxIterations = DefaultMaxIterations
	}
	if c.MaxChanges <= 0 || c.MaxChanges > 3 {
		c.MaxChanges = DefaultMaxChanges
	}
	if c.Tolerance <= 0 {
		c.Tolerance = DefaultTolerance
	}
	if c.RenderSize <= 0 {
		c.RenderSize = DefaultRenderSize
	}
}

// Run executes the refinement loop:
//
//	analyze references → (plan + blockout if the model is empty) → repeat:
//	  render → measure + critique → advance the stage when its thresholds pass,
//	  otherwise apply ≤ MaxChanges corrections → render → measure → revert if worse
func (c *Controller) Run(ctx context.Context, goal string, imageIDs []string) (*Result, error) {
	c.defaults()
	a, vs := c.App, c.App.Visual
	if strings.TrimSpace(goal) != "" {
		vs.Goal = strings.TrimSpace(goal)
	}
	for _, id := range imageIDs {
		im, ok := a.Images.Get(id)
		if !ok {
			return nil, fmt.Errorf("image %q was not uploaded", id)
		}
		vs.Lock()
		vs.AddReference(id, im.Width, im.Height)
		vs.Unlock()
	}
	if len(vs.References) == 0 {
		return nil, fmt.Errorf("visual refinement needs at least one reference image")
	}
	res := &Result{}
	pid := a.RecordPrompt("Visual refinement: " + vs.Goal)
	a.RecordPromptImages(pid, imageIDs)
	finish := func(state, summary string) (*Result, error) {
		res.State, res.Summary, res.Stage = state, summary, visual.Stages[min(vs.Stage, len(visual.Stages)-1)].Name
		res.Usage = c.Usage()
		a.SetPromptSummary(summary)
		c.emit(Event{State: state, Message: summary, Stage: res.Stage, Scores: res.Final})
		return res, nil
	}

	// 1. Reference analysis: landmarks, calibration, silhouettes, proportions.
	for _, ref := range vs.References {
		im, ok := a.Images.Get(ref.ImageID)
		if !ok {
			continue // a saved reference whose image was not retained
		}
		if !ref.Analyzed {
			c.emit(Event{State: StateAnalyzing, Message: "Analyzing reference " + ref.ImageID + "…"})
			if err := c.Analyzer.Analyze(ctx, ref, im.Data, im.MediaType, vs.Goal); err != nil {
				return nil, fmt.Errorf("reference analysis: %w", err)
			}
		} else if ref.Mask() == nil && ref.MaskError == "" {
			if m, err := visual.Segment(im.Data); err == nil {
				ref.SetMask(m)
			} else {
				ref.MaskError = err.Error()
			}
		}
	}
	vs.ReferenceGeometry = ReferenceGeometry(vs.References)

	// 2. Strategy and blockout when there is nothing to refine yet.
	m, err := a.Model()
	if err != nil {
		return nil, err
	}
	var built []string
	if len(m.Leaves) == 0 {
		c.emit(Event{State: StatePlanning, Message: "Planning the modeling strategy…"})
		ctxText, _ := json.Marshal(tools.ParameterValues(a.Graph))
		strat, _, err := c.Planner.Plan(ctx, vs.Goal, vs.References, vs.ReferenceGeometry, "Existing design parameters: "+string(ctxText))
		if err != nil {
			return nil, fmt.Errorf("planner: %w", err)
		}
		c.strategy = strat
		vs.Stage = 0
		c.emit(Event{State: StateBuilding, Message: "Building the blockout…", Stage: visual.Stages[0].Name})
		if _, built, err = c.Builder.Build(ctx, a, visual.Stages[0], vs.Goal, strat, vs.ReferenceGeometry); err != nil {
			return nil, err
		}
		if m, _ = a.Model(); len(m.Leaves) == 0 {
			return finish(StateFailed, "The builder did not create any geometry for the blockout.")
		}
	}
	if vs.Stage >= len(visual.Stages) {
		vs.Stage = len(visual.Stages) - 1
	}

	cur, err := c.evaluate(ctx)
	if err != nil {
		return nil, err
	}
	kind := "baseline"
	if len(built) > 0 {
		kind = "build"
	}
	c.record(cur, kind, nil, nil, nil, built, "")
	res.Final = cur.Scores

	// 3. Refinement loop.
	for i := 0; i < c.MaxIterations; i++ {
		if ctx.Err() != nil {
			return finish(StateStopped, "Stopped by the user. "+c.status(cur))
		}
		res.Iterations++
		if cur.Gate.Passed {
			if vs.Stage == len(visual.Stages)-1 {
				return finish(StateCompleted, "Final quality reached. "+c.status(cur))
			}
			vs.Stage++
			stage := visual.Stages[vs.Stage]
			c.emit(Event{State: StateAdvancing, Message: fmt.Sprintf("Stage passed — building %s…", stage.Name), Stage: stage.Name, Scores: cur.Scores})
			before := tools.ParameterValues(a.Graph)
			_, cs, err := c.Builder.Build(ctx, a, stage, vs.Goal, c.strategy, vs.ReferenceGeometry)
			if err != nil {
				return nil, err
			}
			next, err := c.evaluate(ctx)
			if err != nil {
				return nil, err
			}
			c.record(next, "build", nil, before, tools.ParameterValues(a.Graph), cs, "built "+stage.Name)
			cur = next
			res.Final = cur.Scores
			continue
		}
		corrections := c.selectCorrections(cur)
		if len(corrections) == 0 {
			return finish(StateStopped, "No actionable corrections left (all remaining errors are locked or already failed). "+c.status(cur))
		}
		var names []string
		for _, k := range corrections {
			names = append(names, k.Region+": "+k.Problem)
		}
		c.emit(Event{State: StateCorrecting, Message: "Correcting " + strings.Join(names, "; ") + "…", Stage: visual.Stages[vs.Stage].Name, Scores: cur.Scores})
		before := tools.ParameterValues(a.Graph)
		_, cs, err := c.Builder.Apply(ctx, a, visual.Stages[vs.Stage], corrections, c.MaxChanges)
		if err != nil {
			return nil, err
		}
		if len(cs) == 0 {
			vs.Lock()
			for _, k := range corrections {
				vs.RecordFailed(k)
			}
			vs.Unlock()
			c.record(cur, "refine", corrections, before, before, nil, "the builder made no change; these corrections will not be retried")
			continue
		}
		next, err := c.evaluate(ctx)
		if err != nil {
			return nil, err
		}
		it := c.record(next, "refine", corrections, before, tools.ParameterValues(a.Graph), cs, "")
		if next.Overall() < cur.Overall()-c.Tolerance {
			c.emit(Event{State: StateReverting, Message: fmt.Sprintf("Overall score fell from %s to %s — reverting.", visual.Percent(cur.Overall()), visual.Percent(next.Overall())), Iteration: it.N})
			ids, err := a.RevertLatest(cs)
			if len(ids) > 0 && c.OnRevert != nil {
				c.OnRevert(ids)
			}
			if err != nil {
				return nil, err
			}
			vs.Lock()
			it.Reverted, it.Note = true, fmt.Sprintf("reverted: overall %s → %s", visual.Percent(cur.Overall()), visual.Percent(next.Overall()))
			for _, k := range corrections {
				vs.RecordFailed(k)
			}
			vs.Unlock()
			if c.OnIteration != nil {
				c.OnIteration(it)
			}
			continue
		}
		cur = next
		res.Final = cur.Scores
	}
	return finish(StateStopped, fmt.Sprintf("Stopped after %d iterations. ", c.MaxIterations)+c.status(cur))
}

func (c *Controller) status(e *Evaluation) string {
	vs := c.App.Visual
	stage := visual.Stages[min(vs.Stage, len(visual.Stages)-1)]
	s := fmt.Sprintf("Stage %s, overall %s.", stage.Name, visual.Percent(e.Overall()))
	if len(e.Gate.Failing) > 0 {
		s += " Below threshold: " + strings.Join(e.Gate.Failing, ", ") + "."
	}
	return s
}

// selectCorrections takes the most severe critic errors, skipping locked regions and
// corrections that already failed, at most MaxChanges.
func (c *Controller) selectCorrections(e *Evaluation) []visual.Correction {
	if e.Critique == nil {
		return nil
	}
	vs := c.App.Visual
	vs.Lock()
	defer vs.Unlock()
	var out []visual.Correction
	for _, it := range e.Critique.LargestErrors {
		k := visual.Correction{Region: it.Region, Problem: it.Problem, Severity: it.Severity}
		if it.Correction != nil {
			k.Axis, k.DeltaMM, k.Parameter, k.Value = it.Correction.Axis, it.Correction.DeltaMM, it.Correction.Parameter, it.Correction.Value
		}
		if r := vs.Region(it.Region); r != nil && r.Locked {
			continue
		}
		if vs.HasFailed(k) {
			continue
		}
		out = append(out, k)
		if len(out) == c.MaxChanges {
			break
		}
	}
	return out
}

// record appends an iteration (with a checkpoint of the state it describes).
func (c *Controller) record(e *Evaluation, kind string, changes []visual.Correction, before, after map[string]float64, cs []string, note string) *visual.Iteration {
	a, vs := c.App, c.App.Visual
	it := &visual.Iteration{Kind: kind, Renders: map[string]string{}, Silhouettes: e.Silhouettes, Scores: e.Scores, Gate: e.Gate, Critique: e.Raw,
		Changes: changes, ParametersBefore: before, ParametersAfter: after, ChangeSets: cs, Note: note}
	for k, r := range e.Renders {
		it.Renders[k] = r.ID
	}
	vs.Lock()
	it.Stage = visual.Stages[min(vs.Stage, len(visual.Stages)-1)].Name
	vs.AddIteration(it)
	vs.Unlock()
	if cp := a.Checkpoint(fmt.Sprintf("Visual iteration %d (%s)", it.N, it.Stage), true); cp != nil {
		it.Checkpoint = cp.ID
	}
	if c.OnIteration != nil {
		c.OnIteration(it)
	}
	return it
}

// evaluate renders the model (standard views and every reference camera), measures it and
// asks the critic.
func (c *Controller) evaluate(ctx context.Context) (*Evaluation, error) {
	a, vs := c.App, c.App.Visual
	stage := visual.Stages[min(vs.Stage, len(visual.Stages)-1)]
	c.emit(Event{State: StateRendering, Message: "Rendering…", Stage: stage.Name})
	e := &Evaluation{Renders: map[string]*visual.Render{}, Silhouettes: map[string]visual.SilhouetteScore{}, Scores: map[string]float64{}}
	var renders []LabeledImage
	for _, v := range visual.DefaultViews {
		cam, _ := visual.StandardCamera(v)
		r, err := a.RenderCamera(v, cam, c.RenderSize, c.RenderSize, visual.ModeShaded)
		if err != nil {
			return nil, err
		}
		e.Renders[v] = r
		renders = append(renders, LabeledImage{Label: v + " view (" + cam.Projection + ")", Data: r.PNG, MediaType: "image/png"})
	}
	var refs []LabeledImage
	var aspects []float64
	var side *visual.Reference
	var sideRender *visual.Render
	var sideSil visual.SilhouetteScore
	dims := map[string]float64{}
	for _, ref := range vs.References {
		for k, v := range ref.Dimensions {
			dims[k] = v
		}
		im, ok := a.Images.Get(ref.ImageID)
		if !ok || ref.Calibration == nil {
			continue
		}
		key := "ref:" + ref.ImageID
		r, err := a.RenderCamera(key, ref.Calibration.Camera(), c.RenderSize, c.RenderSize, visual.ModeShaded)
		if err != nil {
			return nil, err
		}
		e.Renders[key] = r
		label := fmt.Sprintf("%s view of image %s", ref.Calibration.View, ref.ImageID)
		refs = append(refs, LabeledImage{Label: label, Data: im.Data, MediaType: im.MediaType})
		renderLabel := "render from the camera calibrated to " + label
		if ref.Mask() != nil {
			metric := ref.Calibration.SilhouetteMetric()
			sil := visual.CompareSilhouettes(ref.Mask(), r.Mask, metric == visual.MetricSide || metric == visual.MetricTop || ref.Calibration.Mirrored)
			e.Silhouettes[key] = sil
			if metric != "" {
				e.Scores[metric] = math.Max(e.Scores[metric], sil.Score)
				aspects = append(aspects, sil.BBox)
			}
			if metric == visual.MetricSide && side == nil {
				side, sideRender, sideSil = ref, r, sil
				renderLabel = "side render matching the side reference (image " + ref.ImageID + ")"
			}
		}
		renders = append(renders, LabeledImage{Label: renderLabel, Data: r.PNG, MediaType: "image/png"})
	}

	c.emit(Event{State: StateCritiquing, Message: "Comparing with the references…", Stage: stage.Name, Scores: e.Scores})
	summary, _ := tools.ModelSummary(app.EnvFor(a))
	tree, _ := json.Marshal(summary)
	if len(tree) > 8000 {
		tree = append(tree[:8000], []byte("…")...)
	}
	in := CriticInput{Goal: vs.Goal, Stage: stage, References: refs, Renders: renders, ObjectTree: string(tree),
		Parameters: tools.ParameterValues(a.Graph), Regions: vs.Regions, Dimensions: dims, Metrics: e.Scores}
	if side != nil {
		for k := range side.Landmarks {
			in.SideLandmarkNames = append(in.SideLandmarkNames, k)
		}
	}
	crit, raw, err := c.Critic.Evaluate(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("vision critic: %w", err)
	}
	e.Critique, e.Raw = crit, raw
	e.Scores[visual.MetricSemantic] = round3(crit.OverallScore)

	// Landmarks and proportions from the critic's reading of the matching side render.
	var props []float64
	if side != nil && len(side.Landmarks) > 0 && len(crit.RenderLandmarks) > 0 {
		refBox, genBox := visual.NormBox(side.Mask()), visual.NormBox(sideRender.Mask)
		if s, ok := visual.LandmarkAlignment(side.Landmarks, refBox, crit.RenderLandmarks, genBox, sideSil.Mirrored); ok {
			e.Scores[visual.MetricLandmarks] = s
		}
		if s, ok := visual.LandmarkAlignment(side.Landmarks, refBox, crit.RenderLandmarks, genBox, sideSil.Mirrored, "wheel"); ok {
			e.Scores[visual.MetricWheels] = s
		}
		refRatios := map[string]float64{}
		for k, v := range vs.ReferenceGeometry {
			if !strings.HasSuffix(k, "_aspect") {
				refRatios[k] = v
			}
		}
		if s, per := visual.CompareRatios(refRatios, visual.Ratios(crit.RenderLandmarks, sideRender.Width, sideRender.Height, nil)); len(per) > 0 {
			props = append(props, s)
		}
	}
	props = append(props, aspects...)
	if len(props) > 0 {
		sum := 0.0
		for _, p := range props {
			sum += p
		}
		e.Scores[visual.MetricProportion] = round3(sum / float64(len(props)))
	}
	e.Scores[visual.MetricOverall] = stage.Overall(e.Scores)
	e.Gate = stage.Check(e.Scores)
	return e, nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
