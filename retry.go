package typesafe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"time"
)

// Retry defaults, chosen to match the official Python and JavaScript SDKs
// exactly rather than to be independently reasonable.
//
// A developer porting a working integration from Python should not have to
// re-tune anything, and should not discover that this client gives up sooner
// or hammers harder than the one they came from. Where these numbers look
// arbitrary, it is because they are the official SDK's numbers.
const (
	// DefaultMaxRetries is 2 — three attempts in total.
	DefaultMaxRetries = 2

	// DefaultBackoffInitial is the first delay, doubled on each attempt.
	DefaultBackoffInitial = 500 * time.Millisecond

	// DefaultBackoffMax caps any single delay.
	DefaultBackoffMax = 5 * time.Second

	// DefaultBackoffJitter randomizes each delay by ±25%, so that clients
	// that failed together do not retry together.
	DefaultBackoffJitter = 0.25

	// DefaultRetryTimeout is the budget across all attempts, distinct from
	// the per-operation timeout in DefaultTimeout.
	DefaultRetryTimeout = 30 * time.Second

	// DefaultMaxRetryAfter caps how long a server's retry-after will be
	// honored. Without it, a single header could park a request for minutes;
	// see RetryPolicy.MaxRetryAfter.
	DefaultMaxRetryAfter = 30 * time.Second
)

// RetryPolicy configures retry behavior.
//
// The zero value is not usable — use DefaultRetryPolicy and adjust, or
// WithMaxRetries for the common case.
type RetryPolicy struct {
	// MaxRetries is the number of retries *after* the first attempt. 0
	// disables retrying entirely.
	MaxRetries int

	// BackoffInitial is the first delay. Each subsequent delay doubles.
	BackoffInitial time.Duration

	// BackoffMax caps any single delay.
	BackoffMax time.Duration

	// BackoffJitter randomizes each delay by this fraction, in [0,1].
	// 0.25 means the delay lands uniformly within ±25% of its nominal value.
	BackoffJitter float64

	// HTTPStatuses are the response codes worth retrying. The default is
	// {408, 429, 500–599}, which includes 529.
	//
	// 422 is deliberately absent: a request that failed validation will fail
	// it again, identically, and retrying only delays the error.
	HTTPStatuses map[int]bool

	// RespectRetryAfter honors a retry-after header in place of the computed
	// backoff, subject to MaxRetryAfter and the remaining budget.
	RespectRetryAfter bool

	// MaxRetryAfter caps a server-supplied delay. A retry-after longer than
	// this is treated as "too long to wait" and the error is returned
	// immediately, rather than blocking the caller for an unbounded time on
	// the server's say-so.
	MaxRetryAfter time.Duration

	// RetryOnConnection retries when the request produced no response.
	RetryOnConnection bool

	// RetryOnTimeout retries when an attempt exceeded the per-operation
	// timeout. A caller's own canceled context is never retried.
	RetryOnTimeout bool

	// Predicate, when set, overrides every rule above. Return true to retry.
	//
	// It sees the error from the attempt, including the typed API errors, so
	// a caller can express policies this struct does not cover.
	Predicate func(error) bool

	// Timeout bounds the whole operation across every attempt and every
	// backoff. Zero means no overall budget, and only MaxRetries limits the
	// work.
	Timeout time.Duration

	// rnd supplies jitter. Nil uses the global source. Tests inject a seeded
	// one to make backoff reproducible.
	rnd *rand.Rand
}

// DefaultRetryPolicy returns the policy the client uses when none is set.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:        DefaultMaxRetries,
		BackoffInitial:    DefaultBackoffInitial,
		BackoffMax:        DefaultBackoffMax,
		BackoffJitter:     DefaultBackoffJitter,
		HTTPStatuses:      defaultRetryStatuses(),
		RespectRetryAfter: true,
		MaxRetryAfter:     DefaultMaxRetryAfter,
		RetryOnConnection: true,
		RetryOnTimeout:    true,
		Timeout:           DefaultRetryTimeout,
	}
}

// NoRetry returns a policy that makes exactly one attempt.
func NoRetry() RetryPolicy {
	p := DefaultRetryPolicy()
	p.MaxRetries = 0
	return p
}

func defaultRetryStatuses() map[int]bool {
	m := map[int]bool{http.StatusRequestTimeout: true, http.StatusTooManyRequests: true}
	for s := 500; s < 600; s++ {
		m[s] = true
	}
	return m
}

func (p RetryPolicy) validate() error {
	switch {
	case p.MaxRetries < 0:
		return fmt.Errorf("%w: MaxRetries must not be negative, got %d", ErrInvalidConfig, p.MaxRetries)
	case p.BackoffInitial < 0:
		return fmt.Errorf("%w: BackoffInitial must not be negative, got %s", ErrInvalidConfig, p.BackoffInitial)
	case p.BackoffMax < 0:
		return fmt.Errorf("%w: BackoffMax must not be negative, got %s", ErrInvalidConfig, p.BackoffMax)
	case p.BackoffJitter < 0 || p.BackoffJitter > 1:
		return fmt.Errorf("%w: BackoffJitter must be within [0,1], got %v", ErrInvalidConfig, p.BackoffJitter)
	case p.MaxRetryAfter < 0:
		return fmt.Errorf("%w: MaxRetryAfter must not be negative, got %s", ErrInvalidConfig, p.MaxRetryAfter)
	case p.Timeout < 0:
		return fmt.Errorf("%w: Timeout must not be negative, got %s", ErrInvalidConfig, p.Timeout)
	}
	return nil
}

// backoff returns the nominal delay before attempt n, where n counts retries
// from zero. Jitter is applied by delayFor.
func (p RetryPolicy) backoff(n int) time.Duration {
	if p.BackoffInitial <= 0 {
		return 0
	}
	// Shift rather than math.Pow, and clamp early: doubling a 500ms base
	// overflows int64 at around attempt 54, which a Predicate-driven policy
	// could plausibly reach.
	d := p.BackoffInitial
	for range n {
		if d >= p.BackoffMax || d > math.MaxInt64/2 {
			break
		}
		d *= 2
	}
	if p.BackoffMax > 0 && d > p.BackoffMax {
		d = p.BackoffMax
	}
	return d
}

// delayFor returns the jittered delay before retry n.
func (p RetryPolicy) delayFor(n int) time.Duration {
	d := p.backoff(n)
	if d <= 0 || p.BackoffJitter <= 0 {
		return d
	}
	// Uniform within ±jitter of the nominal delay.
	f := 1 + p.BackoffJitter*(2*p.float64()-1)
	out := time.Duration(float64(d) * f)
	if out < 0 {
		return 0
	}
	return out
}

func (p RetryPolicy) float64() float64 {
	if p.rnd != nil {
		return p.rnd.Float64()
	}
	return rand.Float64()
}

// shouldRetry decides whether err from an attempt is worth another.
func (p RetryPolicy) shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	// A caller who canceled is not asking us to try harder.
	if errors.Is(err, context.Canceled) {
		return false
	}
	if p.Predicate != nil {
		return p.Predicate(err)
	}

	var timeout *TimeoutError
	if errors.As(err, &timeout) {
		return p.RetryOnTimeout
	}
	var conn *ConnectionError
	if errors.As(err, &conn) {
		return p.RetryOnConnection
	}
	var api *APIError
	if errors.As(err, &api) {
		return p.HTTPStatuses[api.Status]
	}
	// Anything else — a malformed response, a client-side validation failure
	// — is deterministic and will not improve on a second look.
	return false
}

// retryAfterFrom extracts a server-requested delay, if the policy honors one.
func (p RetryPolicy) retryAfterFrom(err error) (time.Duration, bool) {
	if !p.RespectRetryAfter {
		return 0, false
	}
	var rl *RateLimitError
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		return rl.RetryAfter, true
	}
	var ov *OverloadedError
	if errors.As(err, &ov) && ov.RetryAfter > 0 {
		return ov.RetryAfter, true
	}
	return 0, false
}

// AttemptInfo describes one completed attempt, passed to a retry observer.
type AttemptInfo struct {
	// Attempt is 1 for the first try, 2 for the first retry, and so on.
	Attempt int

	// Err is what the attempt failed with.
	Err error

	// Status is the HTTP status, or 0 when the attempt produced no response.
	Status int

	// Delay is how long the client will wait before the next attempt.
	Delay time.Duration

	// RetryAfterHonored reports whether Delay came from the server's
	// retry-after header rather than from computed backoff.
	RetryAfterHonored bool

	// Elapsed is the time spent since the first attempt began.
	Elapsed time.Duration
}

// ErrRetriesExhausted wraps the last failure when every attempt was used.
//
// The underlying error remains reachable, so errors.As still finds the
// terminal *RateLimitError or *InternalServerError and errors.Is still matches
// its sentinel. Code that already handles those does not need changing.
var ErrRetriesExhausted = errors.New("typesafe: retries exhausted")

// retriesExhausted joins the sentinel with the last error so both are
// discoverable through the standard errors helpers.
type retriesExhaustedError struct {
	attempts int
	elapsed  time.Duration
	last     error
}

func (e *retriesExhaustedError) Error() string {
	return fmt.Sprintf("typesafe: giving up after %d attempt(s) in %s: %v",
		e.attempts, e.elapsed.Round(time.Millisecond), e.last)
}

func (e *retriesExhaustedError) Unwrap() []error { return []error{ErrRetriesExhausted, e.last} }

// clock abstracts time so that backoff is testable without waiting.
//
// Under Go 1.25's testing/synctest the real clock is already virtual, and the
// synctest-tagged tests use it directly. This interface serves the same tests
// on 1.23 and 1.24, where that package does not exist.
type clock interface {
	Now() time.Time
	// Sleep waits for d or until ctx is done, returning ctx.Err() if it is.
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
