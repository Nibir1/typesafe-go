package typesafe

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrBudgetExceeded means a Budget refused the request. Nothing was sent.
var ErrBudgetExceeded = errors.New("typesafe: budget exceeded")

// Budget caps how much a process may spend against the API.
//
// # What this is for
//
// A retry loop with a bug, a batch job over the wrong input file, a test that
// escapes into CI with a live key — each of these can burn a quota in minutes,
// and the API will cheerfully serve every request until the money or the rate
// limit runs out. A Budget is the thing that says no first.
//
// It fails **before any network I/O**, so an over-limit call costs nothing and
// the error arrives immediately rather than after a rate-limit round trip.
//
//	budget := typesafe.NewBudget(
//	    typesafe.MaxRequestsPerMinute(1200),
//	    typesafe.MaxTokensPerSecond(250_000),
//	    typesafe.MaxTotalRequests(10_000),   // this process, ever
//	)
//	client, err := typesafe.NewClient(typesafe.WithBudget(budget))
//
// # The published limits are defaults, not truth
//
// The rate constants this package exposes come from TypeSafe's Models page,
// which states that they can change without notice during early access. They
// are starting values. A Budget is configured by you, and the SDK never
// assumes a limit it was not given.
//
// Budget is safe for concurrent use and is meant to be shared across every
// client in a process.
type Budget struct {
	mu sync.Mutex

	maxRequestsPerMinute int
	maxTokensPerSecond   int
	maxTotalRequests     int
	maxTotalTokens       int

	requestTimes []time.Time // sliding window, one per request in the last minute
	tokenEvents  []tokenEvent

	totalRequests int
	totalTokens   int

	now func() time.Time
}

type tokenEvent struct {
	at     time.Time
	tokens int
}

// BudgetOption configures a Budget.
type BudgetOption func(*Budget)

// MaxRequestsPerMinute caps the request rate. Zero disables the check.
func MaxRequestsPerMinute(n int) BudgetOption {
	return func(b *Budget) { b.maxRequestsPerMinute = n }
}

// MaxTokensPerSecond caps the estimated token rate. Zero disables the check.
func MaxTokensPerSecond(n int) BudgetOption {
	return func(b *Budget) { b.maxTokensPerSecond = n }
}

// MaxTotalRequests caps requests for the lifetime of this Budget. Zero
// disables the check.
//
// The blunt instrument, and the one that actually stops a runaway loop: a rate
// limit lets a bug spend all day at exactly the permitted speed.
func MaxTotalRequests(n int) BudgetOption {
	return func(b *Budget) { b.maxTotalRequests = n }
}

// MaxTotalTokens caps estimated tokens for the lifetime of this Budget. Zero
// disables the check.
func MaxTotalTokens(n int) BudgetOption {
	return func(b *Budget) { b.maxTotalTokens = n }
}

// NewBudget builds a Budget. With no options it enforces nothing.
func NewBudget(opts ...BudgetOption) *Budget {
	b := &Budget{now: time.Now}
	for _, o := range opts {
		o(b)
	}
	return b
}

// DefaultBudget returns a Budget set to the published Jev 1.13 rate limits.
//
// A convenience, not a guarantee: the limits are documented as changeable, and
// a client at exactly the published rate will still meet 429s under contention
// with other traffic on the same account.
func DefaultBudget() *Budget {
	return NewBudget(
		MaxRequestsPerMinute(DefaultRequestsPerMinute),
		MaxTokensPerSecond(DefaultTokensPerSecond),
	)
}

// BudgetUsage is a snapshot of consumption.
type BudgetUsage struct {
	RequestsLastMinute int
	TokensLastSecond   int
	TotalRequests      int
	TotalTokens        int
}

// Usage returns current consumption.
func (b *Budget) Usage() BudgetUsage {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.evictLocked(now)
	return BudgetUsage{
		RequestsLastMinute: len(b.requestTimes),
		TokensLastSecond:   b.tokensInWindowLocked(),
		TotalRequests:      b.totalRequests,
		TotalTokens:        b.totalTokens,
	}
}

// Reset clears all consumption.
func (b *Budget) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requestTimes = nil
	b.tokenEvents = nil
	b.totalRequests = 0
	b.totalTokens = 0
}

// check reserves capacity for a request of the given estimated size.
//
// Reservation and accounting happen together under one lock, so a hundred
// concurrent callers cannot each observe room for the last request.
func (b *Budget) check(estimatedTokens int) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	b.evictLocked(now)

	if b.maxTotalRequests > 0 && b.totalRequests >= b.maxTotalRequests {
		return fmt.Errorf("%w: %d requests is the configured lifetime cap",
			ErrBudgetExceeded, b.maxTotalRequests)
	}
	if b.maxTotalTokens > 0 && b.totalTokens+estimatedTokens > b.maxTotalTokens {
		return fmt.Errorf("%w: this request's estimated %d tokens would take the total to %d, "+
			"above the lifetime cap of %d",
			ErrBudgetExceeded, estimatedTokens, b.totalTokens+estimatedTokens, b.maxTotalTokens)
	}
	if b.maxRequestsPerMinute > 0 && len(b.requestTimes) >= b.maxRequestsPerMinute {
		oldest := b.requestTimes[0]
		wait := time.Minute - now.Sub(oldest)
		return fmt.Errorf("%w: %d requests in the last minute reaches the cap of %d; "+
			"capacity frees up in %s",
			ErrBudgetExceeded, len(b.requestTimes), b.maxRequestsPerMinute, wait.Round(time.Millisecond))
	}
	if b.maxTokensPerSecond > 0 {
		if inWindow := b.tokensInWindowLocked(); inWindow+estimatedTokens > b.maxTokensPerSecond {
			return fmt.Errorf("%w: %d estimated tokens in the last second plus this request's %d "+
				"would exceed the cap of %d per second",
				ErrBudgetExceeded, inWindow, estimatedTokens, b.maxTokensPerSecond)
		}
	}

	b.requestTimes = append(b.requestTimes, now)
	b.tokenEvents = append(b.tokenEvents, tokenEvent{at: now, tokens: estimatedTokens})
	b.totalRequests++
	b.totalTokens += estimatedTokens
	return nil
}

// evictLocked drops events that have fallen out of their windows.
func (b *Budget) evictLocked(now time.Time) {
	minuteAgo := now.Add(-time.Minute)
	i := 0
	for i < len(b.requestTimes) && b.requestTimes[i].Before(minuteAgo) {
		i++
	}
	if i > 0 {
		b.requestTimes = append(b.requestTimes[:0], b.requestTimes[i:]...)
	}

	secondAgo := now.Add(-time.Second)
	j := 0
	for j < len(b.tokenEvents) && b.tokenEvents[j].at.Before(secondAgo) {
		j++
	}
	if j > 0 {
		b.tokenEvents = append(b.tokenEvents[:0], b.tokenEvents[j:]...)
	}
}

func (b *Budget) tokensInWindowLocked() int {
	var sum int
	for _, e := range b.tokenEvents {
		sum += e.tokens
	}
	return sum
}

// WithBudget attaches a Budget to a client.
//
// Share one Budget across every client in a process that talks to the same
// account: a per-client budget enforces nothing, since the account is what has
// the quota.
func WithBudget(b *Budget) Option {
	return func(c *config) error {
		if b == nil {
			return fmt.Errorf("%w: WithBudget given nil", ErrInvalidConfig)
		}
		c.budget = b
		return nil
	}
}

// WithContextLimitCheck controls whether the client refuses requests whose
// estimated size exceeds a documented ceiling.
//
// On by default. The estimate is conservative and over-reports by roughly 25%,
// so it will occasionally refuse a request the server would have accepted.
// That trade is deliberate — the alternative is a wasted round trip and a 400
// whose message does not say which of the two limits was hit — but turn it off
// if you would rather let the server decide.
func WithContextLimitCheck(enabled bool) Option {
	return func(c *config) error {
		c.checkContextLimit = &enabled
		return nil
	}
}
