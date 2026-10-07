package workflow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	geo "github.com/accelone/simoncad/internal/agent/capabilities/geometry"
	vis "github.com/accelone/simoncad/internal/agent/capabilities/vision"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/agent/workflow"
	"github.com/accelone/simoncad/internal/app"
)

// script returns scripted responses in order and records every request.
type script struct {
	resps []runtime.Response
	reqs  []runtime.Request
}

func (s *script) Name() string { return "script" }
func (s *script) Complete(_ context.Context, r runtime.Request) (*runtime.Response, error) {
	s.reqs = append(s.reqs, r)
	if len(s.resps) == 0 {
		return nil, fmt.Errorf("script exhausted after %d requests", len(s.reqs))
	}
	resp := s.resps[0]
	s.resps = s.resps[1:]
	return &resp, nil
}

var callN int

func call(name, args string) runtime.Response {
	callN++
	return runtime.Response{StopReason: "tool_use", Content: []runtime.Block{{Type: "tool_use", ID: fmt.Sprintf("tu_%d", callN), Name: name, Input: json.RawMessage(args)}}}
}

func say(text string) runtime.Response {
	return runtime.Response{StopReason: "end_turn", Content: []runtime.Block{{Type: "text", Text: text}}}
}

const complexIntent = "Create a 100 x 60 x 4 mm base plate, add a 5 mm radius cylindrical boss on top, and fillet the plate edges"

// plateBossPlan is a two-step plan expecting a 100 x 60 x 4 plate and a boss of radius 5.
const plateBossPlan = `{"goal":"Plate with a boss","analysis":{"primitives":["box","cylinder"]},
 "steps":[{"step":1,"operation":"primitive","description":"Create base plate","ref":"plate"},
          {"step":2,"operation":"primitive","description":"Add cylindrical boss","ref":"boss","depends_on":[1]},
          {"step":3,"operation":"validate","description":"Validate geometry"}],
 "expectations":{"objects":[{"ref":"plate","size":{"x":100,"y":60,"z":4}},{"ref":"boss","parameters":{"radius":5}}],"body_count":2}}`

func newAgent(t *testing.T, prov runtime.Provider, events *[]workflow.Event) (*workflow.Agent, *app.App) {
	t.Helper()
	a := app.New("t", time.Now)
	t.Cleanup(a.Close)
	ag := &workflow.Agent{Runtime: &runtime.Runtime{Provider: prov, Caps: []runtime.Capability{geo.New(), vis.New()}},
		OnState: func(ev workflow.Event) { *events = append(*events, ev) }}
	return ag, a
}

func states(evs []workflow.Event) string {
	var out []string
	for _, e := range evs {
		if len(out) == 0 || out[len(out)-1] != string(e.State) {
			out = append(out, string(e.State))
		}
	}
	return strings.Join(out, ">")
}

func TestNeedsPlan(t *testing.T) {
	for intent, want := range map[string]bool{
		"Make it red":                             false,
		"Move the box 10 mm along X":              false,
		"Delete the selected part":                false,
		"Add four 3.2 mm holes near the corners":  false,
		"a 50 mm cube with 4 mm rounded edges":    false,
		"Change the thickness to 5 mm":            false,
		"Hide the cylinder and make the box blue": false,
		complexIntent:                             true,
		"Create a servo body with a cylindrical housing, subtract four mounting holes, apply 1 mm fillets and make it black ABS": true,
		"1. create a 40 mm cube\n2. cut a 10 mm hole through it":                                                                 true,
	} {
		if got := workflow.NeedsPlan(intent); got != want {
			t.Errorf("NeedsPlan(%q) = %v, want %v", intent, got, want)
		}
	}
}

func TestSimpleRequestSkipsPlanning(t *testing.T) {
	var evs []workflow.Event
	prov := &script{resps: []runtime.Response{call("create_box", `{"dx":10,"dy":10,"dz":10}`), say("Created a cube.")}}
	ag, a := newAgent(t, prov, &evs)
	res, err := ag.Run(context.Background(), a, "Make a 10 mm cube", app.PolicyCommit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan != nil || len(prov.reqs) != 2 || res.Final != "Created a cube." || res.State != workflow.StateCompleted {
		t.Fatalf("plan=%v reqs=%d final=%q state=%s", res.Plan, len(prov.reqs), res.Final, res.State)
	}
	if got := states(evs); got != "EXECUTING>COMPLETED" {
		t.Fatalf("states: %s", got)
	}
}

func TestPlanExecuteValidateCorrect(t *testing.T) {
	var evs []workflow.Event
	prov := &script{resps: []runtime.Response{
		call("submit_plan", plateBossPlan),
		// step 1 builds the plate 20 mm too short
		call("create_box", `{"id":"plate","dx":80,"dy":60,"dz":4}`), say("Created plate (80 x 60 x 4 mm)."),
		call("create_cylinder", `{"id":"boss","radius":5,"height":10,"x":40,"y":30,"z":4}`), say("Added boss r=5."),
		// correction: edit the existing feature, do not rebuild
		call("modify_parameter", `{"feature":"plate","parameter":"dx","value":100}`), say("Set the plate length to 100 mm."),
	}}
	var plans int
	ag, a := newAgent(t, prov, &evs)
	ag.OnPlan = func(*workflow.Plan) { plans++ }
	res, err := ag.Run(context.Background(), a, complexIntent, app.PolicyCommit)
	if err != nil {
		t.Fatal(err)
	}
	if plans != 1 || res.Plan == nil || len(res.Plan.Steps) != 3 {
		t.Fatalf("plan: %+v", res.Plan)
	}
	if res.State != workflow.StateCompleted || !res.Validation.OK || res.Attempts != 1 {
		t.Fatalf("state=%s attempts=%d issues=%v", res.State, res.Attempts, res.Validation.Issues)
	}
	if got := states(evs); got != "PLANNING>EXECUTING>VALIDATING>CORRECTING>VALIDATING>COMPLETED" {
		t.Fatalf("states: %s", got)
	}
	ex := res.Execution
	if ex.Refs["plate"] != "plate" || ex.Refs["boss"] != "boss" || ex.Steps[0].Status != workflow.StepSuccess || len(ex.Steps[0].Objects) != 1 {
		t.Fatalf("execution: %+v", ex)
	}
	// the planner sees only submit_plan; the executor sees the CAD tools and one step at a time
	if len(prov.reqs[0].Tools) != 1 || prov.reqs[0].Tools[0].Name != "submit_plan" {
		t.Fatal("planner must only be offered submit_plan")
	}
	first := prov.reqs[1].Messages[len(prov.reqs[1].Messages)-1].Content[0].Text
	if !strings.Contains(first, "carry out ONLY step 1") || !strings.Contains(first, "## Plan") {
		t.Fatalf("step 1 directive:\n%s", first)
	}
	step2 := lastUserText(prov.reqs[3])
	if !strings.Contains(step2, "ONLY step 2") || !strings.Contains(step2, "plate = plate") {
		t.Fatalf("step 2 directive must carry the earlier refs:\n%s", step2)
	}
	fix := lastUserText(prov.reqs[5])
	if !strings.Contains(fix, "probably caused by step 1") || !strings.Contains(fix, "size along x is 80 mm, expected 100 mm") {
		t.Fatalf("correction directive:\n%s", fix)
	}
	if !strings.Contains(res.Final, "Validation passed after 1 automatic correction") || len(a.AH.Prompts) != 1 || a.AH.Prompts[0].Summary != res.Final {
		t.Fatalf("final: %q", res.Final)
	}
}

func lastUserText(r runtime.Request) string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		m := r.Messages[i]
		if m.Role == "user" && len(m.Content) > 0 && m.Content[len(m.Content)-1].Type == "text" {
			return m.Content[len(m.Content)-1].Text
		}
	}
	return ""
}

func TestFailedStepSkipsDependentsAndIsReExecuted(t *testing.T) {
	var evs []workflow.Event
	prov := &script{resps: []runtime.Response{
		call("submit_plan", plateBossPlan),
		call("create_box", `{"id":"plate","dx":-1,"dy":60,"dz":4}`), say("The plate could not be created."),
		// step 2 depends on step 1: never sent to the model
		// correction re-executes step 1, then the skipped step 2
		call("create_box", `{"id":"plate","dx":100,"dy":60,"dz":4}`), say("Created plate."),
		call("create_cylinder", `{"id":"boss","radius":5,"height":10,"x":40,"y":30,"z":4}`), say("Added boss."),
	}}
	ag, a := newAgent(t, prov, &evs)
	res, err := ag.Run(context.Background(), a, complexIntent, app.PolicyCommit)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != workflow.StateCompleted || res.Attempts != 1 || !res.Validation.OK {
		t.Fatalf("state=%s attempts=%d issues=%v", res.State, res.Attempts, res.Validation.Issues)
	}
	if len(prov.reqs) != 7 {
		t.Fatalf("the dependent step must not run blindly: %d requests", len(prov.reqs))
	}
	if retry := lastUserText(prov.reqs[3]); !strings.Contains(retry, "step 1 (Create base plate) failed") || !strings.Contains(retry, "ONLY step 1") {
		t.Fatalf("retry directive:\n%s", retry)
	}
}

func TestBrokenCorrectionIsRevertedAndUnresolvedReported(t *testing.T) {
	var evs []workflow.Event
	resps := []runtime.Response{
		call("submit_plan", plateBossPlan),
		call("create_box", `{"id":"plate","dx":80,"dy":60,"dz":4}`), say("Created plate."),
		call("create_cylinder", `{"id":"boss","radius":5,"height":10,"x":40,"y":30,"z":4}`), say("Added boss."),
	}
	for i := 0; i < workflow.MaxAutocorrectionAttempts; i++ { // every "fix" deletes the plate
		resps = append(resps, call("delete_objects", `{"features":["plate"]}`), say("Removed the plate."))
	}
	prov := &script{resps: resps}
	var reverted []string
	ag, a := newAgent(t, prov, &evs)
	ag.OnRevert = func(ids []string) { reverted = append(reverted, ids...) }
	res, err := ag.Run(context.Background(), a, complexIntent, app.PolicyCommit)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != workflow.StateFailed || res.Attempts != workflow.MaxAutocorrectionAttempts || len(reverted) != workflow.MaxAutocorrectionAttempts {
		t.Fatalf("state=%s attempts=%d reverted=%v", res.State, res.Attempts, reverted)
	}
	if a.Snapshot().Get("plate") == nil || a.Snapshot().Get("boss") == nil {
		t.Fatal("the model must be left in its latest valid state")
	}
	if !strings.Contains(res.Final, "Unresolved after 3") || !strings.Contains(res.Final, "expected 100 mm") {
		t.Fatalf("final must explain the unresolved problem: %q", res.Final)
	}
	if states(evs)[len(states(evs))-6:] != "FAILED" {
		t.Fatalf("states: %s", states(evs))
	}
}

func TestStepAwaitingApprovalStopsThePlan(t *testing.T) {
	var evs []workflow.Event
	prov := &script{resps: []runtime.Response{
		call("submit_plan", plateBossPlan),
		call("create_box", `{"id":"plate","dx":100,"dy":60,"dz":4}`), say("Proposed the plate."),
	}}
	ag, a := newAgent(t, prov, &evs)
	res, err := ag.Run(context.Background(), a, complexIntent, app.PolicyPreview)
	if err != nil {
		t.Fatal(err)
	}
	if res.Execution.Steps[0].Status != workflow.StepApproval || res.Execution.Steps[1].Status != workflow.StepPending || len(prov.reqs) != 3 {
		t.Fatalf("execution: %+v (%d requests)", res.Execution.Steps, len(prov.reqs))
	}
	if !strings.Contains(res.Final, "waiting for your approval") || res.Validation != nil {
		t.Fatalf("final: %q", res.Final)
	}
}

func TestPlannerDeclineFallsBackToOneTurn(t *testing.T) {
	var evs []workflow.Event
	prov := &script{resps: []runtime.Response{say("What size should the plate be?"), say("What size should the plate be?")}}
	ag, a := newAgent(t, prov, &evs)
	res, err := ag.Run(context.Background(), a, complexIntent, app.PolicyCommit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan != nil || res.Final != "What size should the plate be?" || len(prov.reqs) != 2 {
		t.Fatalf("plan=%v final=%q", res.Plan, res.Final)
	}
}

func TestValidatorChecksAppearanceAndBodyCount(t *testing.T) {
	a := app.New("t", time.Now)
	defer a.Close()
	for _, c := range []struct{ tool, args string }{
		{"create_box", `{"id":"base","dx":20,"dy":20,"dz":5}`},
		{"set_material", `{"features":["base"],"material_id":"black_abs"}`},
	} {
		if o := a.ApplyTool(c.tool, []byte(c.args), app.PolicyCommit); !o.OK {
			t.Fatalf("%s: %s", c.tool, o.Error)
		}
	}
	plan := &workflow.Plan{Steps: []workflow.Step{{N: 1, Operation: "primitive", Description: "Create base", Ref: "base"}, {N: 2, Operation: "material", Description: "Apply material", Target: "base"}},
		Expectations: workflow.Expectations{BodyCount: 1, Objects: []workflow.ObjectExpectation{{Ref: "base", Material: "black_abs", Min: []float64{0, 0, 0}}}}}
	ex := workflow.NewExecution(plan)
	ex.Steps[0].Status, ex.Steps[1].Status = workflow.StepSuccess, workflow.StepSuccess
	ex.Refs["base"] = "base"
	rep, err := workflow.DefaultValidator.Validate(a, plan, ex)
	if err != nil || !rep.OK {
		t.Fatalf("valid model: %v %v", err, rep.Issues)
	}
	plan.Expectations = workflow.Expectations{BodyCount: 2, Objects: []workflow.ObjectExpectation{{Ref: "base", Color: "#FF0000", Center: []float64{50, 50, 50}}}}
	rep, _ = workflow.DefaultValidator.Validate(a, plan, ex)
	kinds := map[string]int{}
	for _, is := range rep.Issues {
		kinds[is.Kind] = is.Step
	}
	if rep.OK || kinds[workflow.IssueBodyCount] == 0 || kinds[workflow.IssueAppearance] != 2 || kinds[workflow.IssuePosition] != 1 {
		t.Fatalf("issues: %+v", rep.Issues)
	}
}
