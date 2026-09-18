package typesafe

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"
	"sync"
	"time"
)

// Questions is a named set of questions, for readability at a call site where
// the map type would otherwise dominate the line.
type Questions = map[string]Question

// Batch defaults.
const (
	// DefaultBatchConcurrency is how many requests run at once when nothing
	// else is specified. Deliberately modest: the published rate limits are
	// generous, but they are shared with everything else on the account, and
	// a batch that saturates them starves the interactive traffic beside it.
	DefaultBatchConcurrency = 8

	// MinAdaptiveConcurrency is the floor adaptive backoff will not go below.
	// One worker still makes progress; zero would deadlock.
	MinAdaptiveConcurrency = 1
)

// ItemResult is the outcome for one state in a batch.
type ItemResult struct {
	// Index is the position in the input slice. Results are returned in input
	// order, so this is redundant there — it matters in the streaming view,
	// where results arrive as they finish.
	Index int

	// State is the input this result belongs to, carried through so a caller
	// handling a failure does not have to index back into the original slice.
	State any

	// Response is the answer set, nil on failure.
	Response *SystemOneResponse

	// Err is the failure, nil on success.
	Err error

	// Duration is how long this item took, retries included.
	Duration time.Duration
}

// BatchResult is the outcome of a whole batch.
type BatchResult struct {
	// Items holds one result per input state, **in input order**.
	Items []ItemResult

	// Usage is the summed token usage across successful items.
	Usage Usage

	// Succeeded and Failed count the outcomes.
	Succeeded int
	Failed    int

	// Duration is the wall-clock time for the whole batch.
	Duration time.Duration

	// PeakConcurrency is the highest number of requests in flight at once,
	// and MinConcurrency the lowest the limit fell to. When adaptive
	// concurrency is on, a gap between them means the batch was throttled.
	PeakConcurrency int
	MinConcurrency  int
}

// Err returns a summary error when any item failed, or nil.
//
// Deliberately *not* the first failure. A batch of a thousand where three
// items failed for two different reasons is badly served by surfacing one of
// them; the summary names how many failed and why, and Errors gives the rest.
func (r BatchResult) Err() error {
	if r.Failed == 0 {
		return nil
	}

	// Group by message so a thousand identical rate-limit errors read as one
	// line rather than a thousand.
	counts := map[string]int{}
	var order []string
	for _, item := range r.Items {
		if item.Err == nil {
			continue
		}
		msg := item.Err.Error()
		if counts[msg] == 0 {
			order = append(order, msg)
		}
		counts[msg]++
	}
	sort.Slice(order, func(i, j int) bool {
		if counts[order[i]] != counts[order[j]] {
			return counts[order[i]] > counts[order[j]]
		}
		return order[i] < order[j]
	})

	var b strings.Builder
	fmt.Fprintf(&b, "typesafe: %d of %d batch items failed", r.Failed, len(r.Items))
	for i, msg := range order {
		if i == 3 {
			fmt.Fprintf(&b, "\n  (and %d other distinct error(s))", len(order)-3)
			break
		}
		fmt.Fprintf(&b, "\n  %d x %s", counts[msg], msg)
	}
	return &BatchError{Summary: b.String(), Failed: r.Failed, Total: len(r.Items)}
}

// Errors returns every item failure, in input order.
func (r BatchResult) Errors() []error {
	out := make([]error, 0, r.Failed)
	for _, item := range r.Items {
		if item.Err != nil {
			out = append(out, item.Err)
		}
	}
	return out
}

// Responses returns the successful responses, in input order.
func (r BatchResult) Responses() []*SystemOneResponse {
	out := make([]*SystemOneResponse, 0, r.Succeeded)
	for _, item := range r.Items {
		if item.Err == nil {
			out = append(out, item.Response)
		}
	}
	return out
}

// BatchError summarizes partial failure.
//
// It wraps nothing: a batch failure is not one error, and pretending it is
// would let errors.As pick an arbitrary item's cause and look authoritative.
// Use BatchResult.Errors to inspect the individual failures.
type BatchError struct {
	// Summary is the human-readable grouping.
	Summary string

	// Failed and Total count the outcome.
	Failed, Total int
}

func (e *BatchError) Error() string { return e.Summary }

// ErrBatchPartialFailure matches any BatchError through errors.Is.
var ErrBatchPartialFailure = errors.New("typesafe: some batch items failed")

// Is reports whether target is ErrBatchPartialFailure.
func (e *BatchError) Is(target error) bool { return target == ErrBatchPartialFailure }

// BatchOption configures a batch.
type BatchOption func(*batchConfig)

type batchConfig struct {
	concurrency int
	adaptive    bool
	model       string
	onItem      func(ItemResult)
}

// WithConcurrency bounds how many requests run at once.
//
// Values below 1 are treated as 1. There is no unbounded mode: a batch that
// launches a goroutine per input is a way to convert a large slice into a rate
// limit error, and the useful ceiling is set by the account's quota rather
// than by the size of the input.
func WithConcurrency(n int) BatchOption {
	return func(c *batchConfig) {
		if n < 1 {
			n = 1
		}
		c.concurrency = n
	}
}

// WithAdaptiveConcurrency turns global rate-limit backoff on or off. On by
// default.
//
// When a worker meets a 429 or a 529, the limit for the *whole batch* halves,
// and it recovers by one after a run of successes. Without this, every worker
// independently rediscovers the same limit, and the batch spends its time in
// per-request backoff while continuing to push at the rate that caused the
// problem.
func WithAdaptiveConcurrency(enabled bool) BatchOption {
	return func(c *batchConfig) { c.adaptive = enabled }
}

// WithBatchModel selects the model for every request in the batch.
//
// SystemOneBatch builds each request itself, so SystemOneRequest.Model is not
// reachable from the call site; without this the client default is the only
// option. Leave it unset to use that default.
func WithBatchModel(model string) BatchOption {
	return func(c *batchConfig) { c.model = model }
}

// WithItemCallback registers a function called as each item completes, in
// completion order.
//
// For progress reporting on a long batch. It runs on the worker's goroutine,
// so keep it quick and make it safe for concurrent use. For processing results
// as they land, prefer SystemOneBatchSeq, which does not require that care.
func WithItemCallback(fn func(ItemResult)) BatchOption {
	return func(c *batchConfig) { c.onItem = fn }
}

// SystemOneBatch evaluates the same questions against many states.
//
//	result := client.SystemOneBatch(ctx, states, typesafe.Questions{
//	    "is_spam":  typesafe.Noul{Instructions: "Is this spam?"},
//	    "sentiment": typesafe.Choice{ ... },
//	}, typesafe.WithConcurrency(16))
//
//	if err := result.Err(); err != nil {
//	    log.Printf("%v", err) // a summary, not one arbitrary failure
//	}
//	for _, item := range result.Items { // input order
//	    ...
//	}
//
// # It batches states, never questions
//
// Jev ingests the state once and evaluates every question against it in
// parallel, so a second question costs only its own tokens while a second
// state costs a whole request. Pack every question about one state into a
// single call and use this to fan out across states — batching questions would
// be strictly more expensive and slower.
//
// # Per-item error isolation
//
// One failure never aborts the batch. Each item carries its own error, and the
// rest continue at full speed. A batch where one bad input discards
// ninety-nine good results is not useful, and retrying the whole thing to
// recover them is worse.
//
// Cancelling ctx stops the batch: items already running finish or fail, and
// items not yet started are returned with the context error. Every result slot
// is filled either way, so Items always has one entry per input.
func (c *Client) SystemOneBatch(ctx context.Context, states []any, qs Questions, opts ...BatchOption) BatchResult {
	cfg := batchConfig{concurrency: DefaultBatchConcurrency, adaptive: true}
	for _, o := range opts {
		o(&cfg)
	}

	start := time.Now()
	result := BatchResult{
		Items:          make([]ItemResult, len(states)),
		MinConcurrency: cfg.concurrency,
	}
	if len(states) == 0 {
		return result
	}

	lim := newAdaptiveLimiter(cfg.concurrency, cfg.adaptive)
	var wg sync.WaitGroup

	for i, state := range states {
		// Stop launching once the caller has given up; the remaining slots are
		// filled below rather than left as zero values, so a caller reading
		// Items never sees a silent success that never happened.
		if err := ctx.Err(); err != nil {
			for j := i; j < len(states); j++ {
				result.Items[j] = ItemResult{Index: j, State: states[j], Err: err}
			}
			break
		}
		if !lim.acquire(ctx) {
			for j := i; j < len(states); j++ {
				result.Items[j] = ItemResult{Index: j, State: states[j], Err: ctx.Err()}
			}
			break
		}

		wg.Add(1)
		go func(i int, state any) {
			defer wg.Done()
			defer lim.release()

			itemStart := time.Now()
			resp, err := c.SystemOne(ctx, &SystemOneRequest{
				State: state, Model: cfg.model, Questions: qs,
			})
			lim.observe(err)

			item := ItemResult{
				Index:    i,
				State:    state,
				Response: resp,
				Err:      err,
				Duration: time.Since(itemStart),
			}
			// Writing to a distinct index needs no lock: each goroutine owns
			// exactly one slot, which is also what keeps results in input
			// order without a sort.
			result.Items[i] = item

			if cfg.onItem != nil {
				cfg.onItem(item)
			}
		}(i, state)
	}

	wg.Wait()

	for _, item := range result.Items {
		if item.Err != nil {
			result.Failed++
			continue
		}
		result.Succeeded++
		if item.Response != nil {
			result.Usage.InputTokens += item.Response.Usage.InputTokens
			result.Usage.OutputTokens += item.Response.Usage.OutputTokens
		}
	}
	result.Duration = time.Since(start)
	result.PeakConcurrency, result.MinConcurrency = lim.stats()
	return result
}

// SystemOneBatchSeq is SystemOneBatch as a stream, yielding results as they
// complete rather than when the whole batch finishes.
//
//	for i, item := range client.SystemOneBatchSeq(ctx, states, qs) {
//	    if item.Err != nil { ... }
//	}
//
// Use it when the batch is large enough that holding every response in memory
// matters, or when downstream work can start on the first result. Results
// arrive in **completion** order; ItemResult.Index gives the input position.
//
// Breaking out of the range stops the batch: the remaining work is cancelled
// and every goroutine exits before the loop returns.
func (c *Client) SystemOneBatchSeq(ctx context.Context, states []any, qs Questions, opts ...BatchOption) iter.Seq2[int, ItemResult] {
	return func(yield func(int, ItemResult) bool) {
		if len(states) == 0 {
			return
		}

		cfg := batchConfig{concurrency: DefaultBatchConcurrency, adaptive: true}
		for _, o := range opts {
			o(&cfg)
		}

		// A break in the caller's loop cancels the workers. Without this, the
		// goroutines would run to completion writing into a channel nobody
		// reads — which is the leak this whole shape exists to avoid.
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		results := make(chan ItemResult, cfg.concurrency)
		var wg sync.WaitGroup

		go func() {
			lim := newAdaptiveLimiter(cfg.concurrency, cfg.adaptive)
			for i, state := range states {
				if ctx.Err() != nil || !lim.acquire(ctx) {
					// Report what will not be attempted, so the consumer sees
					// one result per input even when cut short.
					for j := i; j < len(states); j++ {
						select {
						case results <- ItemResult{Index: j, State: states[j], Err: context.Cause(ctx)}:
						case <-ctx.Done():
							wg.Wait()
							close(results)
							return
						}
					}
					break
				}

				wg.Add(1)
				go func(i int, state any) {
					defer wg.Done()
					defer lim.release()

					itemStart := time.Now()
					resp, err := c.SystemOne(ctx, &SystemOneRequest{
						State: state, Model: cfg.model, Questions: qs,
					})
					lim.observe(err)

					item := ItemResult{
						Index: i, State: state, Response: resp, Err: err,
						Duration: time.Since(itemStart),
					}
					select {
					case results <- item:
					case <-ctx.Done():
					}
				}(i, state)
			}
			wg.Wait()
			close(results)
		}()

		for item := range results {
			if !yield(item.Index, item) {
				// The caller broke. Cancel and drain so every worker's send
				// completes and every goroutine exits before returning.
				cancel()
				for range results {
				}
				return
			}
		}
	}
}

// adaptiveLimiter bounds concurrency and shrinks it when the API pushes back.
//
// The limit is shared across every worker in one batch. That is the point: a
// per-worker limit means N workers each discover the same rate limit
// independently, each back off in isolation, and the batch keeps pushing at
// the rate that caused the problem.
type adaptiveLimiter struct {
	mu        sync.Mutex
	limit     int
	ceiling   int // the configured limit; recovery never exceeds it
	inFlight  int
	adaptive  bool
	successes int

	peak int
	min  int

	// waiters are signalled when a slot frees or the limit grows.
	cond *sync.Cond
}

func newAdaptiveLimiter(limit int, adaptive bool) *adaptiveLimiter {
	if limit < 1 {
		limit = 1
	}
	l := &adaptiveLimiter{limit: limit, ceiling: limit, adaptive: adaptive, min: limit}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// acquire takes a slot, blocking until one is free. It returns false when ctx
// is done.
func (l *adaptiveLimiter) acquire(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}

	l.mu.Lock()
	// Fast path: a free slot needs no watchdog. This is the common case on a
	// large batch — one goroutine per item just to watch a context that is
	// never cancelled would cost more than the work it guards.
	if l.inFlight < l.limit {
		l.take()
		l.mu.Unlock()
		return true
	}
	l.mu.Unlock()

	// Slow path. sync.Cond has no deadline, so a cancelled context would leave
	// the waiter parked forever; a watchdog broadcasts to wake it.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			l.cond.Broadcast()
		case <-done:
		}
	}()

	l.mu.Lock()
	defer l.mu.Unlock()
	for l.inFlight >= l.limit {
		if ctx.Err() != nil {
			return false
		}
		l.cond.Wait()
	}
	if ctx.Err() != nil {
		return false
	}
	l.take()
	return true
}

// take claims a slot. The caller holds l.mu.
func (l *adaptiveLimiter) take() {
	l.inFlight++
	if l.inFlight > l.peak {
		l.peak = l.inFlight
	}
}

func (l *adaptiveLimiter) release() {
	l.mu.Lock()
	l.inFlight--
	l.mu.Unlock()
	l.cond.Broadcast()
}

// observe feeds an item's outcome back into the limit.
func (l *adaptiveLimiter) observe(err error) {
	if !l.adaptive {
		return
	}

	throttled := errors.Is(err, ErrRateLimit) || errors.Is(err, ErrOverloaded) ||
		errors.Is(err, ErrBudgetExceeded)

	l.mu.Lock()
	defer l.mu.Unlock()

	if throttled {
		// Halve rather than decrement: pushback means the current rate is
		// wrong by some margin, and stepping down one at a time takes N
		// round trips to find a limit that multiplicative decrease reaches in
		// log N.
		l.limit /= 2
		if l.limit < MinAdaptiveConcurrency {
			l.limit = MinAdaptiveConcurrency
		}
		l.successes = 0
		if l.limit < l.min {
			l.min = l.limit
		}
		return
	}
	if err != nil {
		return // an ordinary failure says nothing about the rate
	}

	// Recover additively, and slowly. Doubling back up after a throttle just
	// rediscovers the same wall.
	//
	// Recovery stops at the configured limit. WithConcurrency(n) is a ceiling
	// the caller chose — often because something else shares the account's
	// quota — and a limiter that climbs past it on a run of successes has
	// quietly overridden the one number the caller gave it.
	if l.limit >= l.ceiling {
		return
	}
	l.successes++
	if l.successes >= 2*l.limit {
		l.successes = 0
		l.limit++
		l.cond.Broadcast()
	}
}

func (l *adaptiveLimiter) stats() (peak, min int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.peak, l.min
}
