package typesafe

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// The breaker's own state machine is driven through allow/record with explicit
// timestamps, so its time-dependent transitions are tested without waiting and
// without a clock abstraction leaking into its public surface.

func retryableErr(status int) error {
	return newAPIError("POST /v1/systemone", status, http.Header{}, nil, nil)
}

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	b := &CircuitBreaker{Threshold: 3, OpenFor: time.Minute}
	now := time.Now()

	for i := range 2 {
		if err := b.allow(now); err != nil {
			t.Fatalf("failure %d: circuit should still be closed: %v", i, err)
		}
		b.record(now, retryableErr(503), true)
	}
	if err := b.allow(now); err != nil {
		t.Fatalf("still below the threshold: %v", err)
	}
	b.record(now, retryableErr(503), true)

	err := b.allow(now)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err = %v, want ErrCircuitOpen after %d failures", err, b.Threshold)
	}
	if got := b.State(); got != CircuitOpen {
		t.Errorf("State = %s, want open", got)
	}
}

// TestSuccessResetsTheCount: the threshold counts *consecutive* failures. An
// intermittent error rate should not eventually trip a breaker that is mostly
// succeeding.
func TestSuccessResetsTheCount(t *testing.T) {
	b := &CircuitBreaker{Threshold: 3, OpenFor: time.Minute}
	now := time.Now()

	for range 10 {
		b.record(now, retryableErr(500), true)
		b.record(now, retryableErr(500), true)
		b.record(now, nil, false) // recovered
	}
	if err := b.allow(now); err != nil {
		t.Errorf("circuit opened on non-consecutive failures: %v", err)
	}
}

// TestNonRetryableFailuresDoNotOpenIt: a 422 means the request was wrong, not
// that the service is unwell. Tripping on those would let one caller's bad
// input open the circuit for everyone sharing the breaker.
func TestNonRetryableFailuresDoNotOpenIt(t *testing.T) {
	b := &CircuitBreaker{Threshold: 2, OpenFor: time.Minute}
	now := time.Now()

	for range 20 {
		b.record(now, retryableErr(422), false)
	}
	if err := b.allow(now); err != nil {
		t.Errorf("circuit opened on validation failures: %v", err)
	}
	if got := b.State(); got != CircuitClosed {
		t.Errorf("State = %s, want closed", got)
	}
}

func TestBreakerRecoversThroughHalfOpen(t *testing.T) {
	var changes []string
	b := &CircuitBreaker{
		Threshold: 2,
		OpenFor:   time.Minute,
		OnStateChange: func(from, to CircuitState) {
			changes = append(changes, from.String()+"->"+to.String())
		},
	}
	t0 := time.Now()

	b.record(t0, retryableErr(503), true)
	b.record(t0, retryableErr(503), true)
	if err := b.allow(t0); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("should be open: %v", err)
	}

	// Still open just before the window expires.
	if err := b.allow(t0.Add(59 * time.Second)); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("should still be open at 59s: %v", err)
	}

	// One probe is allowed once the window passes.
	later := t0.Add(61 * time.Second)
	if err := b.allow(later); err != nil {
		t.Fatalf("a probe should be allowed after the window: %v", err)
	}
	// And only one.
	if err := b.allow(later); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("a second concurrent probe should be refused: %v", err)
	}

	// The probe succeeds: back to closed.
	b.record(later, nil, false)
	if got := b.State(); got != CircuitClosed {
		t.Errorf("State = %s, want closed after a successful probe", got)
	}
	if err := b.allow(later); err != nil {
		t.Errorf("traffic should flow again: %v", err)
	}

	want := []string{"closed->open", "open->half-open", "half-open->closed"}
	if len(changes) != len(want) {
		t.Fatalf("state changes = %v, want %v", changes, want)
	}
	for i := range want {
		if changes[i] != want[i] {
			t.Errorf("change %d = %q, want %q", i, changes[i], want[i])
		}
	}
}

// TestFailedProbeReopensImmediately: a half-open probe that fails must not
// require another full threshold's worth of failures to re-open.
func TestFailedProbeReopensImmediately(t *testing.T) {
	b := &CircuitBreaker{Threshold: 2, OpenFor: time.Minute}
	t0 := time.Now()

	b.record(t0, retryableErr(503), true)
	b.record(t0, retryableErr(503), true)

	probe := t0.Add(61 * time.Second)
	if err := b.allow(probe); err != nil {
		t.Fatalf("probe should be allowed: %v", err)
	}
	b.record(probe, retryableErr(503), true)

	if err := b.allow(probe); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("a failed probe should re-open at once: %v", err)
	}
	// And the clock restarts from the failed probe, not the original opening.
	if err := b.allow(probe.Add(59 * time.Second)); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("the open window should restart from the failed probe: %v", err)
	}
}

func TestBreakerReset(t *testing.T) {
	b := &CircuitBreaker{Threshold: 1, OpenFor: time.Hour}
	now := time.Now()
	b.record(now, retryableErr(500), true)
	if err := b.allow(now); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal("should be open")
	}
	b.Reset()
	if err := b.allow(now); err != nil {
		t.Errorf("Reset should close the circuit: %v", err)
	}
}

func TestBreakerValidation(t *testing.T) {
	for name, b := range map[string]*CircuitBreaker{
		"negative threshold": {Threshold: -1},
		"negative openfor":   {OpenFor: -time.Second},
		"negative probes":    {HalfOpenProbes: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(WithAPIKey("k"), WithCircuitBreaker(b)); err == nil {
				t.Error("NewClient should reject an invalid breaker")
			} else if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("err = %v, want ErrInvalidConfig", err)
			}
		})
	}
	if _, err := NewClient(WithAPIKey("k"), WithCircuitBreaker(nil)); err == nil {
		t.Error("a nil breaker should be rejected")
	}
}

func TestBreakerIsRaceFree(t *testing.T) {
	b := NewCircuitBreaker()
	b.Threshold = 3
	b.OpenFor = time.Millisecond

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			now := time.Now()
			_ = b.allow(now)
			if i%3 == 0 {
				b.record(now, nil, false)
			} else {
				b.record(now, retryableErr(503), true)
			}
			_ = b.State()
		}(i)
	}
	wg.Wait()
}

// --- through the client ------------------------------------------------------

func TestClientStopsSendingWhenTheCircuitOpens(t *testing.T) {
	srv, calls, _ := scriptedServer(t, status(503))
	breaker := &CircuitBreaker{Threshold: 2, OpenFor: time.Hour}

	p := noJitter()
	p.MaxRetries = 0 // one attempt per call, to count them plainly
	c := retryClient(t, srv.URL, newFakeClock(), WithRetryPolicy(p), WithCircuitBreaker(breaker))

	for i := range 2 {
		if _, err := c.SystemOne(context.Background(), newRequest()); err == nil {
			t.Fatalf("call %d should have failed", i)
		}
	}
	if got := calls(); got != 2 {
		t.Fatalf("server saw %d calls, want 2", got)
	}

	// The circuit is now open: further calls must not reach the network.
	_, err := c.SystemOne(context.Background(), newRequest())
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err = %v, want ErrCircuitOpen", err)
	}
	if got := calls(); got != 2 {
		t.Errorf("server saw %d calls; an open circuit must send nothing", got)
	}
}

// TestOpenCircuitKeepsTheUnderlyingCause: rejecting a retry because the
// circuit opened mid-sequence should still tell the caller what was failing.
func TestOpenCircuitKeepsTheUnderlyingCause(t *testing.T) {
	srv, _, _ := scriptedServer(t, status(503))
	breaker := &CircuitBreaker{Threshold: 1, OpenFor: time.Hour}

	p := noJitter()
	p.MaxRetries = 3
	c := retryClient(t, srv.URL, newFakeClock(), WithRetryPolicy(p), WithCircuitBreaker(breaker))

	_, err := c.SystemOne(context.Background(), newRequest())
	if err == nil {
		t.Fatal("expected failure")
	}
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("err should report the open circuit: %v", err)
	}
	var ise *InternalServerError
	if !errors.As(err, &ise) {
		t.Errorf("err should still carry the 503 that opened it: %v", err)
	}
}

func TestNoBreakerByDefault(t *testing.T) {
	srv, calls, _ := scriptedServer(t, status(503))
	p := noJitter()
	p.MaxRetries = 0
	c := retryClient(t, srv.URL, newFakeClock(), WithRetryPolicy(p))

	for range 20 {
		_, _ = c.SystemOne(context.Background(), newRequest())
	}
	if got := calls(); got != 20 {
		t.Errorf("server saw %d calls, want all 20 — no breaker is attached by default", got)
	}
}
