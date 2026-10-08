package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The error a model that has no "none" level answers with.
const noneRefused = `openai API 400: Unsupported value: 'none' is not supported with the ` +
	`'gpt-6.1-sol' model. Supported values are: 'low', 'medium', 'high', 'xhigh', and 'max'.`

func TestSupportedEffortsFromError(t *testing.T) {
	got := supportedEfforts(noneRefused)
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if len(got) != len(want) {
		t.Fatalf("supported = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("supported = %v, want %v", got, want)
		}
	}
	if supportedEfforts("openai API 500: upstream unavailable") != nil {
		t.Fatal("an unrelated error listed supported values")
	}
}

func TestAltEffortPicksTheWeakestWhenNoneIsRefused(t *testing.T) {
	alt, ok := altEffort(noneRefused, "none")
	if !ok || alt != "low" {
		t.Fatalf("alt = %q ok=%v, want low", alt, ok)
	}
}

func TestAltEffortIgnoresUnrelatedErrors(t *testing.T) {
	for _, msg := range []string{
		"openai API 401: invalid api key",
		"openai API 400: Unsupported value: 'xyz' is not supported with the 'm' model.",             // not what we sent
		"openai API 400: Unsupported value: 'none' is not supported with the 'm' model.",            // lists nothing
		"openai API 400: Unsupported value: 'none' is not supported. Supported values are: 'none'.", // same value back
	} {
		if alt, ok := altEffort(msg, "none"); ok {
			t.Fatalf("%q produced a retry with %q", msg, alt)
		}
	}
}

func TestNearestEffortTiesTowardsLessThinking(t *testing.T) {
	// "medium" refused, "low" and "high" offered: both one step away, take the cheaper.
	if got, _ := nearestEffort("medium", []string{"high", "low"}); got != "low" {
		t.Fatalf("nearest = %q, want low", got)
	}
	if got, _ := nearestEffort("max", []string{"low", "high"}); got != "high" {
		t.Fatalf("nearest = %q, want high", got)
	}
	if _, ok := nearestEffort("none", nil); ok {
		t.Fatal("an empty list produced a substitute")
	}
}

// effortServer refuses the efforts in refuse and records every effort it was sent.
func effortServer(t *testing.T, refuse map[string]bool, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*seen = append(*seen, body.Reasoning.Effort)
		if refuse[body.Reasoning.Effort] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported value: '` + body.Reasoning.Effort +
				`' is not supported with the 'gpt-6.1-sol' model. Supported values are: 'low', 'medium', 'high', 'xhigh', and 'max'."}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Done."}]}],` +
			`"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
}

func TestInstantAnswerFallsBackToTheWeakestSupportedEffort(t *testing.T) {
	var seen []string
	srv := effortServer(t, map[string]bool{"none": true}, &seen)
	defer srv.Close()
	o := &OpenAI{Flavor: "openai", APIKey: "k", Model: "gpt-6.1-sol", BaseURL: srv.URL, HTTP: srv.Client()}

	resp, err := o.Complete(context.Background(), Request{NoThinking: true,
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("the turn failed instead of adapting: %v", err)
	}
	if resp.Text() != "Done." {
		t.Fatalf("text = %q", resp.Text())
	}
	if len(seen) != 2 || seen[0] != "none" || seen[1] != "low" {
		t.Fatalf("efforts sent = %v, want [none low]", seen)
	}

	// The substitution is remembered: the second request costs no extra round trip.
	if _, err := o.Complete(context.Background(), Request{NoThinking: true,
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "again"}}}}}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[2] != "low" {
		t.Fatalf("efforts sent = %v, want the remembered low", seen)
	}
}

func TestAConfiguredEffortIsAlsoAdapted(t *testing.T) {
	var seen []string
	srv := effortServer(t, map[string]bool{"minimal": true}, &seen)
	defer srv.Close()
	o := &OpenAI{Flavor: "openai", APIKey: "k", Model: "gpt-6.1-sol", BaseURL: srv.URL, HTTP: srv.Client(),
		ReasoningEffort: "minimal"}
	if _, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}); err != nil {
		t.Fatalf("the turn failed instead of adapting: %v", err)
	}
	if len(seen) != 2 || seen[1] != "low" {
		t.Fatalf("efforts sent = %v, want a retry at low", seen)
	}
}

func TestAnUnrelatedBadRequestIsNotRetried(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, "call")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"context length exceeded"}}`))
	}))
	defer srv.Close()
	o := &OpenAI{Flavor: "openai", APIKey: "k", Model: "gpt-6.1-sol", BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := o.Complete(context.Background(), Request{NoThinking: true,
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err == nil {
		t.Fatal("the error was swallowed")
	}
	if len(seen) != 1 {
		t.Fatalf("%d calls, want 1 (no retry)", len(seen))
	}
}

func TestNoEffortSentWhenNoneIsConfigured(t *testing.T) {
	var seen []string
	srv := effortServer(t, nil, &seen)
	defer srv.Close()
	o := &OpenAI{Flavor: "openai", APIKey: "k", Model: "gpt-6.1-sol", BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("efforts sent = %v, want the model default", seen)
	}
}
