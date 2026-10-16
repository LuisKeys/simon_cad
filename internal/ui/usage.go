package ui

import (
	"fmt"

	"github.com/accelone/simoncad/internal/agent/runtime"
)

// UsageView is what the interface shows about token consumption: the last agent turn and
// the running session total, each with a cost estimate when prices are configured. Tokens
// are always reported; Cost.Known says whether the amount means anything.
type UsageView struct {
	Turn    TurnUsage `json:"turn"`
	Session TurnUsage `json:"session"`
	// Turns counts the model-driven turns (agent runs and refinement runs) of the session.
	Turns int `json:"turns"`
	// Route is the routing record of the last request: tiers, attempts, escalations, cost and
	// latency (nil before the first request).
	Route *runtime.RouteTrace `json:"route,omitempty"`
}

// TurnUsage is one scope of consumption: tokens, their per-model breakdown and the cost.
type TurnUsage struct {
	InputTokens int `json:"input_tokens"`
	// CachedInputTokens is the part of InputTokens served from the provider's prompt cache.
	CachedInputTokens int                  `json:"cached_input_tokens"`
	OutputTokens      int                  `json:"output_tokens"`
	TotalTokens       int                  `json:"total_tokens"`
	Models            []runtime.ModelUsage `json:"models"`
	Cost              runtime.Cost         `json:"cost"`
	// Text is the one-line human summary, so every surface words it the same way.
	Text string `json:"text"`
}

func turnUsage(u runtime.Usage) TurnUsage {
	models := u.Models()
	if models == nil {
		models = []runtime.ModelUsage{}
	}
	return TurnUsage{InputTokens: u.InputTokens, CachedInputTokens: u.CachedInputTokens,
		OutputTokens: u.OutputTokens, TotalTokens: u.Tokens(),
		Models: models, Cost: runtime.Estimate(u), Text: runtime.Report(u)}
}

// recordUsage adds one finished turn to the session ledger. Caller holds mu.
func (s *Service) recordUsage(u runtime.Usage) {
	s.turnUsage = u
	s.sessionUsage.Add(u)
	if u.Tokens() > 0 {
		s.turns++
	}
}

// usageView renders the ledger for State. Caller holds mu.
func (s *Service) usageView() UsageView {
	return UsageView{Turn: turnUsage(s.turnUsage), Session: turnUsage(s.sessionUsage), Turns: s.turns, Route: s.lastTrace}
}

// sayUsage reports one turn's consumption in the conversation. Caller holds mu.
func (s *Service) sayUsage(u runtime.Usage) {
	if u.Tokens() == 0 {
		return // recorded or stubbed providers report no usage; say nothing rather than "0".
	}
	text := runtime.Report(u)
	if total := s.sessionUsage; total.Tokens() > u.Tokens() {
		text += fmt.Sprintf("\nSession: %s", runtime.Report(total))
	}
	s.say("usage", text, "", true, "")
}
