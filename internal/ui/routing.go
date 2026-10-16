package ui

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/agent/workflow"
	"github.com/accelone/simoncad/internal/config"
)

// ---- agent ----------------------------------------------------------------------------

// router builds the model router of one request with prov as its primary model, the
// session's fallback authorization and the route observer. Caller holds mu.
func (s *Service) router(prov runtime.Provider) *runtime.Router {
	r := s.NewRouter()
	r.Selected = prov
	if (r.Consent == "" || r.Consent == config.ConsentAsk) && s.sessionConsent != "" {
		r.Consent = s.sessionConsent
	}
	r.OnRoute = s.onRoute
	return r
}

// onRoute records which model serves a call and tells the front end (the Simon Agent model
// indicator). It is called from model calls, while mu is released.
func (s *Service) onRoute(ev runtime.RouteEvent) {
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if ev.Fallback {
		known := false
		for _, f := range s.fallbacks {
			known = known || f == ev
		}
		if !known {
			s.fallbacks = append(s.fallbacks, ev)
		}
	}
	if s.model != nil && *s.model == ev {
		return
	}
	s.model = &ev
	s.hub.Publish(Notice{Kind: "model", Model: &ev})
}

// beginRoutes starts the routing record of a request. Caller holds mu.
func (s *Service) beginRoutes() {
	s.routeMu.Lock()
	s.fallbacks = nil
	s.routeMu.Unlock()
	s.announced = map[string]bool{}
}

// announceFallback names a fallback model in the conversation once per request. Caller
// holds mu.
func (s *Service) announceFallback(label, reason string) {
	if s.announced == nil {
		s.announced = map[string]bool{}
	}
	if s.announced[label] {
		return
	}
	s.announced[label] = true
	text := "Fallback: " + label
	if reason != "" {
		text += " — " + reason
	}
	s.say("system", text, "", true, "")
}

// endRoutes closes the routing record of a request that consumed u: every fallback is
// named in the conversation and logged with the model asked for, the model used, why, and
// what it consumed. Caller holds mu.
func (s *Service) endRoutes(u runtime.Usage) {
	s.routeMu.Lock()
	fbs := s.fallbacks
	s.fallbacks = nil
	s.routeMu.Unlock()
	for _, ev := range fbs {
		s.announceFallback(ev.Label(), ev.Reason)
		var used runtime.Usage
		if m, ok := u.ByModel[runtime.ModelKey(ev.Provider, ev.Model)]; ok {
			used = runtime.Usage{InputTokens: m.InputTokens, CachedInputTokens: m.CachedInputTokens, OutputTokens: m.OutputTokens,
				ByModel: map[string]runtime.ModelUsage{runtime.ModelKey(ev.Provider, ev.Model): m}}
		}
		cost := runtime.Estimate(used).String()
		if cost == "" {
			cost = "not priced"
		}
		log.Printf("model fallback: requested=%q used=%q reason=%q tokens=%d cost=%s", ev.Requested, ev.Label(), ev.Reason, used.Tokens(), cost)
	}
}

// AuthorizeFallback records the answer to the remote fallback warning. remember saves it
// in the settings; otherwise an authorization lasts for this session and a refusal only
// cancels the request that asked.
func (s *Service) AuthorizeFallback(allow, remember bool) error {
	v := config.ConsentDeny
	if allow {
		v = config.ConsentAllow
	}
	if remember {
		if err := config.SetFallbackConsent(v); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if allow {
		s.sessionConsent = config.ConsentAllow
	} else {
		s.sessionConsent = ""
	}
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// unlocking releases the service lock while the model thinks, so reads stay responsive.
type unlocking struct {
	runtime.Provider
	mu *sync.Mutex
}

func (u unlocking) Complete(ctx context.Context, r runtime.Request) (*runtime.Response, error) {
	u.mu.Unlock()
	defer u.mu.Lock()
	return u.Provider.Complete(ctx, r)
}

// Capabilities reports the wrapped provider's capabilities.
func (u unlocking) Capabilities() runtime.ProviderCapabilities {
	return runtime.CapabilitiesOf(u.Provider)
}

func autonomousFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(config.Get("SIMONCAD_AUTONOMOUS"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// route classifies a request: the tier it starts on, why, and whether the user asked for the
// frontier model. A request that asks for it in a Local only configuration is refused with
// a clear error, never run on the local model as if it had been honored. Caller holds mu.
func (s *Service) route(prov runtime.Provider, router *runtime.Router, text string, images, docs bool) (tier runtime.Tier, reason string, asked bool, err error) {
	asked = runtime.WantsFrontier(text)
	if asked && router.Mode == config.ModeLocalOnly {
		return runtime.TierLocal, "", true, runtime.ErrLocalOnly
	}
	sig := runtime.Signals{Images: images, Documents: docs, UserAsked: asked}
	var classifier runtime.Provider
	if router.Mode != "" && router.Mode != config.ModeManual {
		// The manual mode keeps the selected provider unless something is attached or the
		// prompt asks; the other modes also weigh the request's complexity. Only a local
		// model ever classifies: the decision itself must not cost anything.
		sig.Complex = workflow.NeedsPlan(text)
		if runtime.IsLocal(prov) {
			classifier = unlocking{prov, &s.mu}
		}
	}
	tier, reason = runtime.Classify(context.Background(), classifier, text, sig)
	return tier, reason, asked, nil
}

// recordTrace keeps the routing record of the last request and logs it as one JSON line. The
// trace holds no prompt text or project data. Caller holds mu.
func (s *Service) recordTrace(t runtime.RouteTrace) {
	s.lastTrace = &t
	if b, err := json.Marshal(t); err == nil {
		log.Printf("route %s", b)
	}
}
