package typesafe

// Retry tests live in the package rather than in typesafe_test, because they
// inject a clock through the unexported withClock option. Waiting out real
// backoff would make this suite take minutes and still not prove the delays
// were the right length.

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeClock records every sleep instead of performing it, so a test can assert
// the exact backoff sequence and finish instantly.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration

	// interrupt, when set, is returned by the nth Sleep call (1-based) instead
	// of advancing, standing in for a cancelled context mid-backoff.
	interruptAt int
	interrupt   error
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	if c.interruptAt > 0 && len(c.sleeps) == c.interruptAt {
		return c.interrupt
	}
	c.now = c.now.Add(d)
	return nil
}

func (c *fakeClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.sleeps))
	copy(out, c.sleeps)
	return out
}

// noJitter makes backoff exactly reproducible.
func noJitter() RetryPolicy {
	p := DefaultRetryPolicy()
	p.BackoffJitter = 0
	return p
}

func newRequest() *SystemOneRequest {
	return &SystemOneRequest{
		State:     "x",
		Questions: map[string]Question{"q": Noul{Instructions: "?"}},
	}
}

func TestBackoffSequenceIsExponentialAndCapped(t *testing.T) {
	p := noJitter()
	p.BackoffInitial = 100 * time.Millisecond
	p.BackoffMax = 800 * time.Millisecond

	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		800 * time.Millisecond, // capped
		800 * time.Millisecond,
	}
	for i, w := range want {
		if got := p.backoff(i); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i, got, w)
		}
	}
}

// TestBackoffDoesNotOverflow: a Predicate-driven policy can retry far more
// than the default three times, and doubling a duration overflows int64 at
// around attempt 54. An overflow would produce a negative delay.
func TestBackoffDoesNotOverflow(t *testing.T) {
	p := noJitter()
	p.BackoffInitial = time.Second
	p.BackoffMax = time.Minute
	for _, n := range []int{50, 62, 63, 64, 1000} {
		got := p.backoff(n)
		if got < 0 {
			t.Errorf("backoff(%d) = %s, which is negative", n, got)
		}
		if got > p.BackoffMax {
			t.Errorf("backoff(%d) = %s, above the cap of %s", n, got, p.BackoffMax)
		}
	}
}

// TestJitterIsBoundedAndReproducible: jitter must stay within the configured
// fraction, and must be reproducible from a seeded source so that a failing
// backoff test can be re-run.
func TestJitterIsBoundedAndReproducible(t *testing.T) {
	p := DefaultRetryPolicy()
	p.BackoffInitial = time.Second
	p.BackoffMax = time.Second
	p.BackoffJitter = 0.25
	p.rnd = rand.New(rand.NewPCG(1, 2))

	const nominal = time.Second
	lo := time.Duration(float64(nominal) * 0.75)
	hi := time.Duration(float64(nominal) * 1.25)

	var first []time.Duration
	for range 500 {
		d := p.delayFor(0)
		if d < lo || d > hi {
			t.Fatalf("delay %s outside ±25%% of %s", d, nominal)
		}
		first = append(first, d)
	}

	// Same seed, same sequence.
	p.rnd = rand.New(rand.NewPCG(1, 2))
	for i := range first {
		if got := p.delayFor(0); got != first[i] {
			t.Fatalf("delay %d = %s, want %s from the same seed", i, got, first[i])
		}
	}
}

func TestZeroJitterIsExact(t *testing.T) {
	p := noJitter()
	p.BackoffInitial = 250 * time.Millisecond
	for range 100 {
		if got := p.delayFor(0); got != 250*time.Millisecond {
			t.Fatalf("delay = %s, want exactly 250ms with jitter off", got)
		}
	}
}

func TestShouldRetryByStatus(t *testing.T) {
	p := DefaultRetryPolicy()
	cases := map[int]bool{
		400: false, // bad request: the same bytes fail the same way
		401: false,
		403: false,
		404: false,
		408: true,
		422: false, // validation: deterministic
		429: true,
		500: true,
		502: true,
		503: true,
		504: true,
		529: true, // overloaded, explicitly transient
	}
	for status, want := range cases {
		err := newAPIError("POST /v1/systemone", status, http.Header{}, nil, nil)
		if got := p.shouldRetry(err); got != want {
			t.Errorf("status %d: shouldRetry = %v, want %v", status, got, want)
		}
	}
}

func TestShouldRetryByErrorKind(t *testing.T) {
	p := DefaultRetryPolicy()

	if !p.shouldRetry(&ConnectionError{Endpoint: "x", Err: errors.New("refused")}) {
		t.Error("a connection failure should retry by default")
	}
	if !p.shouldRetry(&TimeoutError{Endpoint: "x", Err: context.DeadlineExceeded}) {
		t.Error("a timeout should retry by default")
	}
	if p.shouldRetry(&ResponseValidationError{Endpoint: "x", Err: errors.New("bad")}) {
		t.Error("a malformed response is deterministic and must not retry")
	}
	// A caller who cancelled is not asking us to try harder.
	if p.shouldRetry(context.Canceled) {
		t.Error("a cancelled context must not retry")
	}

	off := DefaultRetryPolicy()
	off.RetryOnConnection = false
	off.RetryOnTimeout = false
	if off.shouldRetry(&ConnectionError{}) || off.shouldRetry(&TimeoutError{}) {
		t.Error("transport retries should be disableable")
	}
}

// TestPredicateOverridesEverything: the escape hatch has to actually escape.
func TestPredicateOverridesEverything(t *testing.T) {
	p := DefaultRetryPolicy()
	p.Predicate = func(error) bool { return true }
	if !p.shouldRetry(newAPIError("x", 422, http.Header{}, nil, nil)) {
		t.Error("a Predicate returning true should retry even a 422")
	}

	p.Predicate = func(error) bool { return false }
	if p.shouldRetry(newAPIError("x", 429, http.Header{}, nil, nil)) {
		t.Error("a Predicate returning false should not retry a 429")
	}
}

func TestPolicyValidation(t *testing.T) {
	bad := map[string]func(*RetryPolicy){
		"negative retries":    func(p *RetryPolicy) { p.MaxRetries = -1 },
		"negative initial":    func(p *RetryPolicy) { p.BackoffInitial = -time.Second },
		"negative max":        func(p *RetryPolicy) { p.BackoffMax = -time.Second },
		"jitter above one":    func(p *RetryPolicy) { p.BackoffJitter = 1.5 },
		"negative jitter":     func(p *RetryPolicy) { p.BackoffJitter = -0.1 },
		"negative retryafter": func(p *RetryPolicy) { p.MaxRetryAfter = -time.Second },
		"negative timeout":    func(p *RetryPolicy) { p.Timeout = -time.Second },
	}
	for name, mutate := range bad {
		t.Run(name, func(t *testing.T) {
			p := DefaultRetryPolicy()
			mutate(&p)
			if err := p.validate(); err == nil {
				t.Fatal("expected a validation error")
			} else if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("err = %v, want ErrInvalidConfig", err)
			}
			if _, err := NewClient(WithAPIKey("k"), WithRetryPolicy(p)); err == nil {
				t.Error("NewClient should reject an invalid policy")
			}
		})
	}
}

func TestDefaultsMatchTheOfficialSDKs(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.MaxRetries != 2 {
		t.Errorf("MaxRetries = %d, want 2 (three attempts total)", p.MaxRetries)
	}
	if p.BackoffInitial != 500*time.Millisecond {
		t.Errorf("BackoffInitial = %s, want 500ms", p.BackoffInitial)
	}
	if p.BackoffMax != 5*time.Second {
		t.Errorf("BackoffMax = %s, want 5s", p.BackoffMax)
	}
	if p.BackoffJitter != 0.25 {
		t.Errorf("BackoffJitter = %v, want 0.25", p.BackoffJitter)
	}
	if p.Timeout != 30*time.Second {
		t.Errorf("Timeout = %s, want 30s", p.Timeout)
	}
	if !p.RespectRetryAfter {
		t.Error("RespectRetryAfter should default to true")
	}
	for _, s := range []int{408, 429, 500, 529, 599} {
		if !p.HTTPStatuses[s] {
			t.Errorf("status %d should be retried by default", s)
		}
	}
	if p.HTTPStatuses[422] {
		t.Error("422 must not be retried")
	}
}

// --- behavior through the client ---------------------------------------------
//
// These drive a real httptest server with an injected clock, so the whole path
// runs — request, status mapping, typed error, retry decision, backoff — while
// finishing in microseconds.

// scriptedServer serves the given handlers in order; the last repeats.
func scriptedServer(t *testing.T, handlers ...http.HandlerFunc) (*httptest.Server, func() int, func() [][]byte) {
	t.Helper()
	var (
		mu     sync.Mutex
		n      int
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		i := n
		n++
		mu.Unlock()
		if i >= len(handlers) {
			i = len(handlers) - 1
		}
		handlers[i](w, r)
	}))
	t.Cleanup(srv.Close)
	return srv,
		func() int { mu.Lock(); defer mu.Unlock(); return n },
		func() [][]byte { mu.Lock(); defer mu.Unlock(); return bodies }
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func okAnswer() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.5}},` +
			`"usage":{"input_tokens":1,"output_tokens":1}}`))
	}
}

func retryClient(t *testing.T, url string, clk clock, opts ...Option) *Client {
	t.Helper()
	all := append([]Option{
		WithAPIKey("test-key"), WithBaseURL(url), withClock(clk),
		WithRetryPolicy(noJitter()),
	}, opts...)
	c, err := NewClient(all...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestRetriesThenSucceeds(t *testing.T) {
	srv, calls, _ := scriptedServer(t, status(500), status(503), okAnswer())
	clk := newFakeClock()
	c := retryClient(t, srv.URL, clk)

	resp, err := c.SystemOne(context.Background(), newRequest())
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if resp.Model != "jev-1.13.0" {
		t.Errorf("Model = %q", resp.Model)
	}
	if got := calls(); got != 3 {
		t.Errorf("made %d attempts, want 3", got)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second}
	got := clk.recorded()
	if len(got) != len(want) {
		t.Fatalf("slept %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("backoff %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestRetriesExhausted(t *testing.T) {
	srv, calls, _ := scriptedServer(t, status(500))
	clk := newFakeClock()
	c := retryClient(t, srv.URL, clk)

	_, err := c.SystemOne(context.Background(), newRequest())
	if err == nil {
		t.Fatal("expected failure")
	}
	if got := calls(); got != 3 {
		t.Errorf("made %d attempts, want 3 (1 + 2 retries)", got)
	}
	if !errors.Is(err, ErrRetriesExhausted) {
		t.Errorf("err should match ErrRetriesExhausted: %v", err)
	}
	// The terminal error stays reachable, so existing handling still works.
	var ise *InternalServerError
	if !errors.As(err, &ise) {
		t.Errorf("err = %v, want the underlying *InternalServerError to remain reachable", err)
	}
	if !errors.Is(err, ErrInternalServer) {
		t.Error("the sentinel should still match through the wrapper")
	}
}

func TestNonRetryableIsReturnedImmediately(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 422} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv, calls, _ := scriptedServer(t, status(code))
			clk := newFakeClock()
			c := retryClient(t, srv.URL, clk)

			_, err := c.SystemOne(context.Background(), newRequest())
			if err == nil {
				t.Fatal("expected failure")
			}
			if got := calls(); got != 1 {
				t.Errorf("made %d attempts, want exactly 1", got)
			}
			if errors.Is(err, ErrRetriesExhausted) {
				t.Error("a non-retryable failure must not be reported as exhausted retries")
			}
			if len(clk.recorded()) != 0 {
				t.Errorf("slept %v before a non-retryable failure", clk.recorded())
			}
		})
	}
}

// TestRetriedBodiesAreIdentical: a retry that sends different bytes is a
// different request. Re-marshalling per attempt would risk exactly that, since
// Go iterates maps in a random order.
func TestRetriedBodiesAreIdentical(t *testing.T) {
	srv, calls, bodies := scriptedServer(t, status(500))
	c := retryClient(t, srv.URL, newFakeClock())

	req := &SystemOneRequest{
		State: map[string]any{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5},
		Questions: map[string]Question{
			"q1": Noul{Instructions: "one"},
			"q2": Choice{Instructions: "two", Criteria: Options{"x": "X", "y": "Y", "z": nil}},
			"q3": Score{Instructions: "three", Criteria: Levels{"a", "b", "c"}},
		},
	}
	_, _ = c.SystemOne(context.Background(), req)

	if calls() != 3 {
		t.Fatalf("made %d attempts, want 3", calls())
	}
	sent := bodies()
	for i := 1; i < len(sent); i++ {
		if string(sent[i]) != string(sent[0]) {
			t.Errorf("attempt %d sent different bytes:\n first: %s\n  this: %s", i+1, sent[0], sent[i])
		}
	}
}

func TestRetryAfterIsHonored(t *testing.T) {
	withHeader := func(code int, v string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", v)
			w.WriteHeader(code)
		}
	}
	srv, _, _ := scriptedServer(t, withHeader(429, "2"), okAnswer())
	clk := newFakeClock()
	c := retryClient(t, srv.URL, clk)

	if _, err := c.SystemOne(context.Background(), newRequest()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	got := clk.recorded()
	if len(got) != 1 || got[0] != 2*time.Second {
		t.Errorf("slept %v, want the server's 2s rather than computed backoff", got)
	}
}

// TestRetryAfterBeyondTheCapAborts is the behavior that keeps a single header
// from parking a caller indefinitely. The error is returned immediately, with
// the server's requested delay still on it.
func TestRetryAfterBeyondTheCapAborts(t *testing.T) {
	long := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
	}
	srv, calls, _ := scriptedServer(t, long, okAnswer())
	clk := newFakeClock()
	c := retryClient(t, srv.URL, clk) // MaxRetryAfter defaults to 30s

	start := time.Now()
	_, err := c.SystemOne(context.Background(), newRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the rate-limit error")
	}
	if calls() != 1 {
		t.Errorf("made %d attempts, want 1", calls())
	}
	if len(clk.recorded()) != 0 {
		t.Errorf("slept %v; a 120s retry-after exceeds the 30s cap and must not be waited out", clk.recorded())
	}
	if elapsed > time.Second {
		t.Errorf("took %s of real time", elapsed)
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %T, want *RateLimitError", err)
	}
	if rl.RetryAfter != 120*time.Second {
		t.Errorf("RetryAfter = %s, want the server's 120s preserved on the error", rl.RetryAfter)
	}
}

// TestBackoffNeverOutlastsTheBudget: sleeping past a deadline we already know
// we will miss wastes the caller's time for no possible benefit.
func TestBackoffNeverOutlastsTheBudget(t *testing.T) {
	srv, calls, _ := scriptedServer(t, status(500))
	clk := newFakeClock()

	p := noJitter()
	p.MaxRetries = 10
	p.BackoffInitial = time.Second
	// Budget fits the first 1s backoff but not the second 2s one:
	// 1s elapsed + 2s > 2.5s, so the loop must stop after two attempts.
	p.Timeout = 2500 * time.Millisecond

	c := retryClient(t, srv.URL, clk, WithRetryPolicy(p))
	_, err := c.SystemOne(context.Background(), newRequest())
	if err == nil {
		t.Fatal("expected failure")
	}

	var total time.Duration
	for _, d := range clk.recorded() {
		total += d
	}
	if total >= p.Timeout {
		t.Errorf("slept %s in total, which reaches the %s budget", total, p.Timeout)
	}
	if calls() > 3 {
		t.Errorf("made %d attempts; the budget should have stopped it sooner", calls())
	}
}

func TestObserverSeesEveryRetry(t *testing.T) {
	srv, _, _ := scriptedServer(t, status(500), status(429), okAnswer())
	clk := newFakeClock()

	var (
		mu   sync.Mutex
		seen []AttemptInfo
	)
	c := retryClient(t, srv.URL, clk, WithRetryObserver(func(_ context.Context, a AttemptInfo) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, a)
	}))

	if _, err := c.SystemOne(context.Background(), newRequest()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("observer saw %d retries, want 2", len(seen))
	}
	if seen[0].Attempt != 1 || seen[0].Status != 500 {
		t.Errorf("first = %+v", seen[0])
	}
	if seen[1].Attempt != 2 || seen[1].Status != 429 {
		t.Errorf("second = %+v", seen[1])
	}
	if seen[0].Delay != 500*time.Millisecond || seen[1].Delay != time.Second {
		t.Errorf("delays = %s, %s", seen[0].Delay, seen[1].Delay)
	}
	if seen[1].Elapsed <= seen[0].Elapsed {
		t.Error("Elapsed should advance between retries")
	}
}

// TestCancellationMidBackoffReturnsPromptly: a caller shutting down should not
// wait out a backoff.
func TestCancellationMidBackoffReturnsPromptly(t *testing.T) {
	srv, _, _ := scriptedServer(t, status(500))

	// A real clock here: the point is that a cancel interrupts an actual wait.
	p := noJitter()
	p.BackoffInitial = 30 * time.Second
	p.Timeout = 0
	c := retryClient(t, srv.URL, realClock{}, WithRetryPolicy(p))

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := c.SystemOne(ctx, newRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected failure")
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %s; cancellation should abort the 30s backoff immediately", elapsed)
	}
	// The reported cause should be the API failure that prompted the wait.
	var ise *InternalServerError
	if !errors.As(err, &ise) {
		t.Errorf("err = %v, want the 500 that caused the backoff to remain reachable", err)
	}
}

func TestNoRetryMakesOneAttempt(t *testing.T) {
	srv, calls, _ := scriptedServer(t, status(500))
	clk := newFakeClock()
	c := retryClient(t, srv.URL, clk, WithRetryPolicy(NoRetry()))

	if _, err := c.SystemOne(context.Background(), newRequest()); err == nil {
		t.Fatal("expected failure")
	}
	if got := calls(); got != 1 {
		t.Errorf("made %d attempts with NoRetry, want 1", got)
	}
	if len(clk.recorded()) != 0 {
		t.Errorf("slept %v with NoRetry", clk.recorded())
	}
}

func TestConcurrentRetriesAreRaceFree(t *testing.T) {
	srv, _, _ := scriptedServer(t, status(500), status(500), okAnswer())
	clk := newFakeClock()

	var observed int64
	var mu sync.Mutex
	c := retryClient(t, srv.URL, clk, WithRetryObserver(func(context.Context, AttemptInfo) {
		mu.Lock()
		observed++
		mu.Unlock()
	}))

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.SystemOne(context.Background(), newRequest())
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if observed == 0 {
		t.Error("no retries observed under concurrent load")
	}
}
