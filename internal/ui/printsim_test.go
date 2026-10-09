package ui

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/printsim/job"
	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

func simSvc(t *testing.T) *Service {
	t.Helper()
	s := newSvc(t)
	if err := s.SetLevel(LevelAuto); err != nil {
		t.Fatal(err)
	}
	return s
}

// waitSim returns the first terminal printsim notice.
func waitSim(t *testing.T, ch <-chan Notice) *job.Event {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Kind == "printsim" && n.PrintSim != nil {
				switch n.PrintSim.State {
				case job.StateDone, job.StateError, job.StateCancelled:
					return n.PrintSim
				}
			}
		case <-deadline:
			t.Fatal("no terminal printsim notice")
		}
	}
}

func TestPrintSimSlicesWithoutTouchingTheModel(t *testing.T) {
	s := simSvc(t)
	if _, err := s.CallTool("create_box", json.RawMessage(`{"id":"b","dx":20,"dy":15,"dz":4}`)); err != nil {
		t.Fatal(err)
	}
	before, _ := s.PrintSimInfo()
	if !before.HasModel || before.GeomHash == "" {
		t.Fatalf("info: %+v", before)
	}
	var builtin bool
	for _, p := range before.Providers {
		if p.Name == "builtin" && p.Available && p.Preview {
			builtin = true
		}
	}
	if !builtin {
		t.Fatalf("the preview slicer must always be available: %+v", before.Providers)
	}
	if before.Profile.Label == "" || !strings.Contains(before.Profile.Label, "not verified") {
		t.Fatalf("the default profile must be labelled a simulation profile: %q", before.Profile.Label)
	}
	live := s.app.K.LiveShapes()
	histBefore := len(s.app.Committed())

	ch, cancel := s.Hub().Subscribe()
	defer cancel()
	st, err := s.PrintSimSlice(PrintSimSliceRequest{Provider: "builtin"})
	if err != nil {
		t.Fatal(err)
	}
	ev := waitSim(t, ch)
	if ev.State != job.StateDone || ev.Job != st.Job {
		t.Fatalf("%+v", ev)
	}
	sum := ev.Summary
	if sum.Layers != 20 || sum.GeomHash != before.GeomHash || !sum.Preview || sum.Disclaimer == "" {
		t.Fatalf("summary %+v", sum)
	}
	bin, err := s.PrintSimResult(st.Job)
	if err != nil {
		t.Fatal(err)
	}
	res, err := toolpath.Decode(bin)
	if err != nil || len(res.Layers) != 20 {
		t.Fatalf("decode: %v", err)
	}
	// the CAD model is untouched: same geometry, same history, nothing leaked in the kernel
	after, _ := s.PrintSimInfo()
	if after.GeomHash != before.GeomHash || len(s.app.Committed()) != histBefore {
		t.Fatal("slicing changed the model")
	}
	if n := s.app.K.LiveShapes(); n != live {
		t.Fatalf("kernel shapes leaked: %d → %d", live, n)
	}
	// same model and profile → cache
	st2, err := s.PrintSimSlice(PrintSimSliceRequest{Provider: "builtin"})
	if err != nil || !st2.Cached {
		t.Fatalf("second slice should be cached: %+v %v", st2, err)
	}
	if ev := waitSim(t, ch); !ev.Cached || ev.Job != st2.Job {
		t.Fatalf("a cache hit must announce itself: %+v", ev)
	}
	if n := s.app.K.LiveShapes(); n != live {
		t.Fatalf("cache hit leaked a shape: %d → %d", live, n)
	}
	// editing the model makes the result stale by hash
	if _, err := s.SetParameter("b", "dz", 6.0); err != nil {
		t.Fatal(err)
	}
	edited, _ := s.PrintSimInfo()
	if edited.GeomHash == sum.GeomHash {
		t.Fatal("a geometry edit must change the revision the UI compares against")
	}
	live = s.app.K.LiveShapes() // the rebuild cache now also holds the edited body
	st3, err := s.PrintSimSlice(PrintSimSliceRequest{Provider: "builtin"})
	if err != nil || st3.Cached {
		t.Fatalf("a modified model must be re-sliced: %+v %v", st3, err)
	}
	if ev := waitSim(t, ch); ev.State != job.StateDone || ev.Summary.Layers != 30 {
		t.Fatalf("%+v", ev)
	}
	if n := s.app.K.LiveShapes(); n != live {
		t.Fatalf("kernel shapes leaked after the second slice: %d → %d", live, n)
	}
}

func TestPrintSimRejectsBadInput(t *testing.T) {
	s := simSvc(t)
	if _, err := s.PrintSimSlice(PrintSimSliceRequest{}); err == nil || !strings.Contains(err.Error(), "no solid geometry") {
		t.Fatalf("empty model: %v", err)
	}
	s.CallTool("create_box", json.RawMessage(`{"id":"b","dx":20,"dy":15,"dz":4}`))
	bad := PrintSimSliceRequest{Provider: "builtin"}
	p := defaultProfileForTest()
	p.LayerHeight = 5
	bad.Profile = &p
	if _, err := s.PrintSimSlice(bad); err == nil || !strings.Contains(err.Error(), "layer_height") {
		t.Fatalf("invalid profile: %v", err)
	}
	if _, err := s.PrintSimSlice(PrintSimSliceRequest{Provider: "cura"}); err == nil || !strings.Contains(err.Error(), "unknown slicer") {
		t.Fatalf("unknown provider: %v", err)
	}
	// a model larger than the bed is reported by the job, in plain words
	s.CallTool("create_box", json.RawMessage(`{"id":"big","dx":400,"dy":10,"dz":10}`))
	ch, cancel := s.Hub().Subscribe()
	defer cancel()
	if _, err := s.PrintSimSlice(PrintSimSliceRequest{Provider: "builtin"}); err != nil {
		t.Fatal(err)
	}
	if ev := waitSim(t, ch); ev.State != job.StateError || !strings.Contains(ev.Error, "build volume") {
		t.Fatalf("%+v", ev)
	}
	if _, err := s.PrintSimResult(ev0(t, s)); err == nil {
		t.Fatal("no result for a failed job")
	}
}

// ev0 returns the id of the most recent job.
func ev0(t *testing.T, s *Service) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sim().State().Job
}

func TestPrintSimHTTP(t *testing.T) {
	srv, ts := testServer(t)
	srv.Svc.SetLevel(LevelAuto)
	srv.Svc.CallTool("create_box", json.RawMessage(`{"id":"b","dx":10,"dy":10,"dz":2}`))
	if code, _ := call(t, ts, "", "POST", "/api/printsim/slice", `{}`); code != http.StatusForbidden {
		t.Fatalf("the token is required: %d", code)
	}
	code, info := call(t, ts, srv.Token, "GET", "/api/printsim/info", "")
	if code != 200 || info["has_model"] != true {
		t.Fatalf("%d %v", code, info)
	}
	ch, cancel := srv.Svc.Hub().Subscribe()
	defer cancel()
	code, out := call(t, ts, srv.Token, "POST", "/api/printsim/slice", `{"provider":"builtin"}`)
	if code != http.StatusAccepted || out["job"] == "" {
		t.Fatalf("%d %v", code, out)
	}
	ev := waitSim(t, ch)
	if ev.State != job.StateDone {
		t.Fatalf("%+v", ev)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/printsim/result/"+out["job"].(string), nil)
	req.Header.Set("X-Simon-Token", srv.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/octet-stream" || string(b[:4]) != "SCPS" {
		t.Fatalf("result: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if code, _ := call(t, ts, srv.Token, "GET", "/api/printsim/result/ps_9999", ""); code != http.StatusNotFound {
		t.Fatalf("unknown job: %d", code)
	}
	if code, _ := call(t, ts, srv.Token, "POST", "/api/printsim/cancel", `{}`); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
	if code, out := call(t, ts, srv.Token, "POST", "/api/printsim/slice", `{"provider":"cura"}`); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "unknown slicer") {
		t.Fatalf("%d %v", code, out)
	}
}

func TestPrintSimStopsBeforeProjectSwitch(t *testing.T) {
	s := simSvc(t)
	s.CallTool("create_box", json.RawMessage(`{"id":"b","dx":20,"dy":20,"dz":20}`))
	s.PrintSimInfo() // warm the rebuild cache so the baseline is stable
	live := s.app.K.LiveShapes()
	st, err := s.PrintSimSlice(PrintSimSliceRequest{Provider: "builtin"})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.stopPrintSim() // what attach and Close do before the kernel goes away
	s.mu.Unlock()
	if r, _ := s.PrintSimResult(st.Job); r != nil {
		t.Fatal("no result may outlive the project")
	}
	if n := s.app.K.LiveShapes(); n != live {
		t.Fatalf("the job must release its kernel shape before the project closes: %d → %d", live, n)
	}
}

func defaultProfileForTest() profile.PrintProfile { return profile.GenericFDM() }
