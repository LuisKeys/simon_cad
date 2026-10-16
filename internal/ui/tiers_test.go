package ui

import (
	"errors"
	"testing"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/config"
)

// tierSvc is a service whose router has a local primary and a cloud standard model, in the
// given routing mode and with the given saved consent.
func tierSvc(t *testing.T, mode, consent string) (*Service, identified, identified) {
	t.Helper()
	s := newSvc(t)
	local := identified{&capProvider{name: "ollama:gemma", caps: textCaps}, "ollama", "gemma"}
	cloud := identified{&capProvider{name: "openai:small", caps: allCaps}, "openai", "small"}
	s.NewProvider = func() (runtime.Provider, error) { return local, nil }
	s.NewRouter = func() *runtime.Router {
		return &runtime.Router{Mode: mode, Consent: consent, Fallbacks: []runtime.Provider{cloud}}
	}
	return s, local, cloud
}

func TestRoutineRequestRunsLocallyAtNoCost(t *testing.T) {
	s, local, cloud := tierSvc(t, config.ModeLocalFirst, config.ConsentAllow)
	if err := s.Prompt("create a box 20 mm wide", false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if len(local.reqs) != 1 || len(cloud.reqs) != 0 {
		t.Fatalf("a routine request stays local: local %d, cloud %d", len(local.reqs), len(cloud.reqs))
	}
	tr := s.usageView().Route
	if tr == nil || tr.InitialTier != "local" || tr.FinalTier != "local" || tr.EstimatedAPICost != 0 || tr.RequestID == "" || !tr.ExecutionSuccess || tr.Mode != config.ModeLocalFirst {
		t.Fatalf("trace: %+v", tr)
	}
}

func TestComplexRequestGoesToTheStandardTierOnlyWhenAuthorized(t *testing.T) {
	complexPrompt := "1. create a base plate\n2. add four holes\n3. fillet the edges and then extrude a rim"
	s, local, cloud := tierSvc(t, config.ModeLocalFirst, config.ConsentAsk)
	if err := s.Prompt(complexPrompt, false); !errors.Is(err, runtime.ErrConsentRequired) {
		t.Fatalf("authorization comes before anything is sent: %v", err)
	}
	if len(cloud.reqs) != 0 || len(local.reqs) != 0 {
		t.Fatal("nothing was sent anywhere")
	}
	if err := s.AuthorizeFallback(true, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Prompt(complexPrompt, false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if len(cloud.reqs) == 0 || len(local.reqs) != 0 {
		t.Fatalf("the complex request runs on the standard tier: local %d, cloud %d", len(local.reqs), len(cloud.reqs))
	}
	if tr := s.usageView().Route; tr.InitialTier != "cloud_standard" {
		t.Fatalf("trace: %+v", tr)
	}
}

// Declining the cloud in Settings must not make complex requests impossible: only the
// complexity asked for it, so the request runs locally.
func TestDeclinedCloudKeepsAComplexRequestLocal(t *testing.T) {
	s, local, cloud := tierSvc(t, config.ModeLocalFirst, config.ConsentDeny)
	if err := s.Prompt("1. create a base plate\n2. add four holes\n3. fillet the edges and then extrude a rim", false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if len(local.reqs) == 0 || len(cloud.reqs) != 0 {
		t.Fatalf("local %d, cloud %d", len(local.reqs), len(cloud.reqs))
	}
	// an attached document, on the other hand, needs the cloud: refused, not silently local
	if _, err := s.AddAttachment("notes.md", []byte("thickness 4 mm")); err != nil {
		t.Fatal(err)
	}
	if err := s.Prompt("create a box", false); err == nil {
		t.Fatal("a declined cloud with a document attached must be an error")
	}
}

func TestLocalOnlyRefusesTheFrontierModelAndNeverSendsOut(t *testing.T) {
	s, local, cloud := tierSvc(t, config.ModeLocalOnly, config.ConsentAllow)
	s.NewRouter = func() *runtime.Router {
		return &runtime.Router{Mode: config.ModeLocalOnly, Consent: config.ConsentAllow, FallbackErr: runtime.ErrLocalOnly}
	}
	if err := s.Prompt("use the frontier model to create a box", false); !errors.Is(err, runtime.ErrLocalOnly) {
		t.Fatalf("an explicit request for the cloud in local-only mode is an error: %v", err)
	}
	if err := s.Prompt("create a box", false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if len(local.reqs) != 1 || len(cloud.reqs) != 0 {
		t.Fatalf("local %d, cloud %d", len(local.reqs), len(cloud.reqs))
	}
}

func TestCloudPreferredStartsOnTheConfiguredCloudModel(t *testing.T) {
	s, local, cloud := tierSvc(t, config.ModeCloudPreferred, config.ConsentAsk)
	if err := s.Prompt("create a box", false); err != nil {
		t.Fatalf("choosing the mode is the authorization: %v", err)
	}
	waitIdle(t, s)
	if len(cloud.reqs) == 0 || len(local.reqs) != 0 {
		t.Fatalf("local %d, cloud %d", len(local.reqs), len(cloud.reqs))
	}
}

// The classifier call is local: with a cloud primary nothing is asked of the model to route.
func TestClassifierNeverUsesACloudModel(t *testing.T) {
	s := newSvc(t)
	cloud := identified{&capProvider{name: "claude:x", caps: allCaps}, "claude", "x"}
	s.NewProvider = func() (runtime.Provider, error) { return cloud, nil }
	s.NewRouter = func() *runtime.Router { return &runtime.Router{Mode: config.ModeLocalFirst} }
	if err := s.Prompt("I need something to hold my phone on the desk, but nicer looking", false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	for _, r := range cloud.reqs {
		if r.ForceTool == "classify_request" {
			t.Fatal("routing must not cost an API call")
		}
	}
}
