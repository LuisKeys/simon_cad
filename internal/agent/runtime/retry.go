package runtime

import (
	"context"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// maxAttempts is how many times a provider call is tried before the provider is declared
// unavailable (see unavailable).
const maxAttempts = 4

// maxRetryWait caps how long a retry-after header may make a request wait.
const maxRetryWait = 30 * time.Second

// jitter returns a factor in [0,1); a variable so tests can pin it.
var jitter = rand.Float64

// retryDelay is the wait before attempt number attempt (1 = first retry): exponential
// (1s, 2s, 4s) with up to 50% added jitter so concurrent clients do not retry in lockstep.
// A server-provided retry-after wins when it asks for longer.
func retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	d := time.Duration(1<<attempt) * 500 * time.Millisecond
	d += time.Duration(float64(d) * 0.5 * jitter())
	if retryAfter > d {
		d = retryAfter
	}
	if d > maxRetryWait {
		d = maxRetryWait
	}
	return d
}

// retryAfter reads the retry-after header in its seconds form (the only one the model APIs
// send); anything else counts as absent.
func retryAfter(h http.Header) time.Duration {
	if s, err := strconv.Atoi(h.Get("retry-after")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 0
}

// waitRetry sleeps before a retry, or returns the context's error if it ends first. The
// first attempt (attempt 0) never waits.
func waitRetry(ctx context.Context, attempt int, after time.Duration) error {
	if attempt == 0 {
		return nil
	}
	t := time.NewTimer(retryDelay(attempt, after))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
