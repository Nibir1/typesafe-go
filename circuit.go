package typesafe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrCircuitOpen means the breaker is open and the request was not attempted.
//
// It is not an API failure: nothing was sent. Treat it as a signal to shed
// load or serve a fallback, not as evidence about this particular request.
var ErrCircuitOpen = errors.New("typesafe: circuit breaker is open")

// Circuit breaker defaults.
const (
	// DefaultCircuitThreshold is how many consecutive retryable failures open
	// the circuit.
	DefaultCircuitThreshold = 5

	// DefaultCircuitOpenFor is how long it stays open before probing.
	DefaultCircuitOpenFor = 30 * time.Second
)

// CircuitState is a breaker's current state.
type CircuitState int

// Breaker states.
const (
	// CircuitClosed passes every request through. The normal state.
	CircuitClosed CircuitState = iota

	// CircuitOpen rejects immediately, without attempting the request.
	CircuitOpen

	// CircuitHalfOpen lets a limited number of probes through to discover
	// whether the service has recovered.
	CircuitHalfOpen
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreaker stops a client from hammering a service that is already
// failing.
//
// Retries help with a blip and hurt during an outage: every caller politely
// backing off and trying again still multiplies load on something that cannot
// serve it. The breaker converts sustained failure into immediate, cheap
// rejection, which both protects the service and lets a caller fail over
// quickly instead of waiting out a full retry budget per request.
//
// It is off by default. Enable it with WithCircuitBreaker, and share one
// breaker per upstream — a breaker per goroutine observes nothing useful.
//
// Only failures the retry policy considers retryable count toward opening it.
// A 422 means the request was wrong, not that the service is unwell, and
// tripping on those would open the circuit for one caller's bad input.
type CircuitBreaker struct {
	// Threshold is the number of consecutive retryable failures that open the
	// circuit. Zero uses DefaultCircuitThreshold.
	Threshold int

	// OpenFor is how long the circuit stays open before allowing a probe.
	// Zero uses DefaultCircuitOpenFor.
	OpenFor time.Duration

	// HalfOpenProbes is how many requests may pass while half-open. Zero
	// means one.
	HalfOpenProbes int

	// OnStateChange, if set, is called whenever the state changes. It runs on
	// the calling goroutine, so keep it quick.
	OnStateChange func(from, to CircuitState)

	mu           sync.Mutex
	state        CircuitState
	failures     int
	openedAt     time.Time
	probes       int
	probeSuccess int
}

// NewCircuitBreaker returns a breaker with the default settings.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		Threshold: DefaultCircuitThreshold,
		OpenFor:   DefaultCircuitOpenFor,
	}
}

func (b *CircuitBreaker) validate() error {
	switch {
	case b.Threshold < 0:
		return fmt.Errorf("%w: circuit threshold must not be negative, got %d", ErrInvalidConfig, b.Threshold)
	case b.OpenFor < 0:
		return fmt.Errorf("%w: circuit OpenFor must not be negative, got %s", ErrInvalidConfig, b.OpenFor)
	case b.HalfOpenProbes < 0:
		return fmt.Errorf("%w: circuit HalfOpenProbes must not be negative, got %d", ErrInvalidConfig, b.HalfOpenProbes)
	}
	return nil
}

func (b *CircuitBreaker) threshold() int {
	if b.Threshold <= 0 {
		return DefaultCircuitThreshold
	}
	return b.Threshold
}

func (b *CircuitBreaker) openFor() time.Duration {
	if b.OpenFor <= 0 {
		return DefaultCircuitOpenFor
	}
	return b.OpenFor
}

func (b *CircuitBreaker) halfOpenProbes() int {
	if b.HalfOpenProbes <= 0 {
		return 1
	}
	return b.HalfOpenProbes
}

// State reports the current state, moving an expired open circuit to
// half-open as a side effect.
func (b *CircuitBreaker) State() CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stateLocked(time.Now())
}

func (b *CircuitBreaker) stateLocked(now time.Time) CircuitState {
	if b.state == CircuitOpen && now.Sub(b.openedAt) >= b.openFor() {
		b.transition(CircuitHalfOpen)
		b.probes, b.probeSuccess = 0, 0
	}
	return b.state
}

// transition records a state change and notifies, with b.mu already held.
func (b *CircuitBreaker) transition(to CircuitState) {
	from := b.state
	if from == to {
		return
	}
	b.state = to
	if b.OnStateChange != nil {
		b.OnStateChange(from, to)
	}
}

// allow reports whether a request may proceed.
func (b *CircuitBreaker) allow(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.stateLocked(now) {
	case CircuitOpen:
		return fmt.Errorf("%w: %d consecutive failures, retrying in %s",
			ErrCircuitOpen, b.failures, (b.openFor() - now.Sub(b.openedAt)).Round(time.Millisecond))
	case CircuitHalfOpen:
		if b.probes >= b.halfOpenProbes() {
			return fmt.Errorf("%w: probing, %d probe(s) already in flight", ErrCircuitOpen, b.probes)
		}
		b.probes++
		return nil
	default:
		return nil
	}
}

// record updates the breaker with the outcome of an attempt. retryable says
// whether the failure was the kind that indicates an unwell service.
func (b *CircuitBreaker) record(now time.Time, err error, retryable bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err == nil {
		switch b.state {
		case CircuitHalfOpen:
			b.probeSuccess++
			if b.probeSuccess >= b.halfOpenProbes() {
				b.transition(CircuitClosed)
				b.failures, b.probes, b.probeSuccess = 0, 0, 0
			}
		default:
			b.failures = 0
		}
		return
	}

	// A request the service rejected on its merits says nothing about the
	// service's health.
	if !retryable {
		return
	}

	switch b.state {
	case CircuitHalfOpen:
		// The probe failed: the service is still unwell.
		b.transition(CircuitOpen)
		b.openedAt = now
		b.probes, b.probeSuccess = 0, 0
	default:
		b.failures++
		if b.failures >= b.threshold() {
			b.transition(CircuitOpen)
			b.openedAt = now
		}
	}
}

// Reset returns the breaker to closed and clears its counters.
func (b *CircuitBreaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.transition(CircuitClosed)
	b.failures, b.probes, b.probeSuccess = 0, 0, 0
}

// WithCircuitBreaker attaches a breaker, which is off by default.
//
// Share one breaker across every client that talks to the same upstream. A
// breaker observes consecutive failures, and one that sees only a fraction of
// the traffic will not trip when it should.
//
//	breaker := typesafe.NewCircuitBreaker()
//	breaker.OnStateChange = func(from, to typesafe.CircuitState) {
//	    log.Warn("typesafe circuit", "from", from, "to", to)
//	}
//	client, err := typesafe.NewClient(typesafe.WithCircuitBreaker(breaker))
//
// When open, SystemOne returns ErrCircuitOpen without sending anything.
func WithCircuitBreaker(b *CircuitBreaker) Option {
	return func(c *config) error {
		if b == nil {
			return fmt.Errorf("%w: WithCircuitBreaker given nil", ErrInvalidConfig)
		}
		if err := b.validate(); err != nil {
			return err
		}
		c.breaker = b
		return nil
	}
}

// guard is the client's hook into the breaker. A nil breaker is a no-op, so
// the call sites stay free of conditionals.
func (c *Client) guardOpen(ctx context.Context) error {
	if c.breaker == nil {
		return nil
	}
	if err := c.breaker.allow(c.clk.Now()); err != nil {
		c.log(ctx, slog.LevelWarn, "typesafe request rejected by the circuit breaker",
			"state", c.breaker.State().String())
		return err
	}
	return nil
}

func (c *Client) guardRecord(err error) {
	if c.breaker == nil {
		return
	}
	c.breaker.record(c.clk.Now(), err, c.retry.shouldRetry(err))
}
