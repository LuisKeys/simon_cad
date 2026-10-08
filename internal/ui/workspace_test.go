package ui

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/workspace"
)

// The Settings screen and the agent change workspace settings through the same tools, so
// the same request gives the same state, the same validation error and the same file.
func TestWorkspaceUIAndAgentAreConsistent(t *testing.T) {
	calls := []struct{ name, args string }{
		{"set_units", `{"linear":"in","angular":"rad","linear_precision":3}`},
		{"configure_grid", `{"spacing":5,"plane":"XZ","origin":[10,0,0],"visible":false}`},
		{"configure_snapping", `{"enabled":true,"rotation":true,"rotation_increment":45,"translation_increment":10}`},
	}

	ui := newSvc(t)
	for _, c := range calls {
		o, err := ui.CallTool(c.name, json.RawMessage(c.args))
		if err != nil || !o.OK {
			t.Fatalf("UI %s: %v %s", c.name, err, o.Error)
		}
	}
	bad, _ := ui.CallTool("configure_grid", json.RawMessage(`{"spacing":0}`))

	agent := newSvc(t)
	script := []string{}
	for _, c := range calls {
		script = append(script, c.name+" "+c.args)
	}
	agent.NewProvider = scripted(t, script...)
	if err := agent.Prompt("switch to inches, a 5 mm XZ grid, snap every 10 mm and 45°", false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, agent)
	agentBad := agent.app.ApplyTool("configure_grid", []byte(`{"spacing":0}`), app.PolicyAuto)

	us, _ := ui.State()
	as, _ := agent.State()
	if !us.Workspace.Settings.Equal(as.Workspace.Settings) {
		t.Fatalf("UI and agent disagree:\nUI    %+v\nagent %+v", us.Workspace.Settings, as.Workspace.Settings)
	}
	if bad.OK || bad.Error != agentBad.Error {
		t.Fatalf("validation differs: UI %q, agent %q", bad.Error, agentBad.Error)
	}
	w := us.Workspace
	if w.Units.Linear != workspace.Inch || w.MMPerUnit != 25.4 || w.Grid.Spacing != 5 || w.Grid.Visible || w.TranslationStep != 10 {
		t.Fatalf("state: %+v", w)
	}
	if !us.Dirty || !as.Dirty {
		t.Fatal("a workspace change must mark the project unsaved")
	}
	// Both persist the same way.
	for _, s := range []*Service{ui, agent} {
		dir, err := s.Save(filepath.Join(t.TempDir(), "p.scadproj"))
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := app.Open(dir, clock)
		if err != nil {
			t.Fatal(err)
		}
		if !reopened.Workspace.Equal(us.Workspace.Settings) {
			t.Fatalf("reloaded %+v", reopened.Workspace)
		}
		reopened.Close()
	}
}
