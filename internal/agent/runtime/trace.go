package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// RouteTrace is the diagnostic record of one request: where it ran, how often it was retried
// or escalated, what it consumed and what it cost. It holds no prompt text, project data or
// model output, so it is safe to log; the classification and escalation reasons are fixed
// phrases, not model-written text.
type RouteTrace struct {
	RequestID   string `json:"request_id"`
	Mode        string `json:"mode"`
	InitialTier string `json:"initial_tier"`
	FinalTier   string `json:"final_tier"`
	// Classification is why the request started on InitialTier.
	Classification string `json:"classification"`
	// Attempts is 1 plus the rounds of tool calls that failed (each failed round is one
	// recovery attempt).
	Attempts         int          `json:"attempts"`
	Escalations      []Escalation `json:"escalations,omitempty"`
	EscalationReason string       `json:"escalation_reason,omitempty"`
	// ExecutionSuccess is the CAD outcome: no provider error, no uncorrected failed call and
	// no escalation that was needed but refused.
	ExecutionSuccess bool `json:"execution_success"`
	// Failure is why the request did not succeed: provider_unavailable, provider_error,
	// escalation_blocked or unresolved_tool_failure.
	Failure string `json:"failure,omitempty"`
	// Models is what each model consumed.
	Models []ModelUsage `json:"models,omitempty"`
	// EstimatedAPICost is the priced spend in Currency; zero for local models. CostKnown is
	// false when a cloud model has no configured price (tokens only).
	EstimatedAPICost float64 `json:"estimated_api_cost"`
	Currency         string  `json:"currency"`
	CostKnown        bool    `json:"cost_known"`
	LocalTokens      int     `json:"local_tokens"`
	CloudTokens      int     `json:"cloud_tokens"`
	// LatencyMS is the whole request; TierLatencyMS the time waiting on each tier's model.
	LatencyMS     int64            `json:"latency_ms"`
	TierLatencyMS map[string]int64 `json:"tier_latency_ms,omitempty"`
}

// TraceInput is what BuildTrace needs to know about a finished request.
type TraceInput struct {
	RequestID string
	Mode      string
	Initial   Tier
	// Reason is why the request started on Initial (a Classify reason).
	Reason  string
	Final   Tier
	Result  *Result
	Err     error
	Elapsed time.Duration
	Latency map[Tier]time.Duration
}

// NewRequestID is a random identifier for a request's trace.
func NewRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// BuildTrace summarizes a finished request. Cost uses the configured prices (Estimate).
func BuildTrace(in TraceInput) RouteTrace {
	res := in.Result
	if res == nil {
		res = &Result{}
	}
	cost := Estimate(res.Usage)
	t := RouteTrace{RequestID: in.RequestID, Mode: in.Mode, InitialTier: in.Initial.String(), FinalTier: in.Final.String(),
		Classification: in.Reason, Attempts: res.Failures + 1, Escalations: res.Escalations, Models: res.Usage.Models(),
		EstimatedAPICost: cost.Amount, Currency: cost.Currency, CostKnown: cost.Known,
		LocalTokens: cost.LocalTokens, CloudTokens: cost.CloudTokens, LatencyMS: in.Elapsed.Milliseconds()}
	if n := len(res.Escalations); n > 0 {
		t.EscalationReason = res.Escalations[n-1].Reason
	}
	if len(in.Latency) > 0 {
		t.TierLatencyMS = map[string]int64{}
		for tier, d := range in.Latency {
			t.TierLatencyMS[tier.String()] = d.Milliseconds()
		}
	}
	switch {
	case in.Err != nil && errors.Is(in.Err, ErrUnavailable):
		t.Failure = "provider_unavailable"
	case in.Err != nil:
		t.Failure = "provider_error"
	case res.Blocked != nil:
		t.Failure = "escalation_blocked"
	case res.Unresolved:
		t.Failure = "unresolved_tool_failure"
	}
	t.ExecutionSuccess = t.Failure == ""
	return t
}
