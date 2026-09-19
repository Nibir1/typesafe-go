package typesafe_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
)

// batchQuestions is one question, asked of every state.
func batchQuestions() typesafe.Questions {
	return typesafe.Questions{
		"is_urgent": typesafe.RawQuestion{
			"type":         "noul",
			"instructions": "Does this convey urgency?",
		},
	}
}

// decodeJSON reads a request body into v.
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func batchStates(n int) []any {
	states := make([]any, n)
	for i := range states {
		states[i] = fmt.Sprintf("state-%d", i)
	}
	return states
}

// echoState answers with the state it was given, so a test can prove a result
// landed in the slot belonging to its input.
func echoState(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State string `json:"state"`
		}
		if err := decodeJSON(r, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"model": "jev-1.13.0",
			"answers": map[string]any{
				"is_urgent": map[string]any{"type": "noul", "noul": 0.5},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 1},
			// Not a real field; carried back so the test can identify the
			// state without relying on arrival order.
			"echo": body.State,
		})
	}
}

// --- ordering ----------------------------------------------------------------

// The headline exit criterion: a thousand states, concurrent, and every result
// in the slot belonging to its input. Run under -race this also covers the
// slot-per-goroutine write pattern.
func TestBatchPreservesInputOrder(t *testing.T) {
	var seen atomic.Int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		echoState(t)(w, r)
	})

	const n = 1000
	states := batchStates(n)

	result := c.SystemOneBatch(context.Background(), states, batchQuestions(),
		typesafe.WithConcurrency(32))

	if err := result.Err(); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if got := int(seen.Load()); got != n {
		t.Errorf("server saw %d requests, want %d", got, n)
	}
	if len(result.Items) != n {
		t.Fatalf("got %d items, want %d", len(result.Items), n)
	}
	for i, item := range result.Items {
		if item.Index != i {
			t.Fatalf("item %d has Index %d", i, item.Index)
		}
		if item.State != states[i] {
			t.Fatalf("item %d carries state %v, want %v", i, item.State, states[i])
		}
		if item.Response == nil {
			t.Fatalf("item %d has no response", i)
		}
	}
	if result.Succeeded != n || result.Failed != 0 {
		t.Errorf("succeeded=%d failed=%d, want %d and 0", result.Succeeded, result.Failed, n)
	}
	if want := n * 10; result.Usage.InputTokens != want {
		t.Errorf("Usage.InputTokens = %d, want %d", result.Usage.InputTokens, want)
	}
}

func TestBatchEmpty(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for an empty batch")
	})
	result := c.SystemOneBatch(context.Background(), nil, batchQuestions())
	if len(result.Items) != 0 || result.Err() != nil {
		t.Errorf("empty batch = %+v, want a zero result", result)
	}
}

// --- error isolation ---------------------------------------------------------

// One 500 in the middle must not take the batch with it, and must not stall
// the others: the failing item is isolated, everything else still succeeds.
func TestBatchIsolatesOneFailure(t *testing.T) {
	const n = 50
	const bad = "state-17"

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State string `json:"state"`
		}
		if err := decodeJSON(r, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.State == bad {
			writeJSON(t, w, http.StatusInternalServerError,
				map[string]any{"detail": "boom"})
			return
		}
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	result := c.SystemOneBatch(context.Background(), batchStates(n), batchQuestions(),
		typesafe.WithConcurrency(8))

	if result.Succeeded != n-1 || result.Failed != 1 {
		t.Fatalf("succeeded=%d failed=%d, want %d and 1", result.Succeeded, result.Failed, n-1)
	}
	if result.Items[17].Err == nil {
		t.Fatal("item 17 should carry an error")
	}
	var apiErr *typesafe.InternalServerError
	if !errors.As(result.Items[17].Err, &apiErr) {
		t.Errorf("item 17 error = %T, want *InternalServerError", result.Items[17].Err)
	}
	for i, item := range result.Items {
		if i == 17 {
			continue
		}
		if item.Err != nil {
			t.Fatalf("item %d failed alongside the bad one: %v", i, item.Err)
		}
	}

	// Err summarizes rather than surfacing one arbitrary failure.
	err := result.Err()
	if err == nil {
		t.Fatal("Err() = nil with a failed item")
	}
	if !errors.Is(err, typesafe.ErrBatchPartialFailure) {
		t.Errorf("Err() should match ErrBatchPartialFailure, got %v", err)
	}
	if want := "1 of 50 batch items failed"; !strings.Contains(err.Error(), want) {
		t.Errorf("Err() = %q, want it to contain %q", err.Error(), want)
	}
	if len(result.Errors()) != 1 || len(result.Responses()) != n-1 {
		t.Errorf("Errors()=%d Responses()=%d, want 1 and %d",
			len(result.Errors()), len(result.Responses()), n-1)
	}
}

// Distinct failure messages are grouped and counted, not listed one per item.
func TestBatchErrGroupsIdenticalFailures(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"detail": "boom"})
	})

	result := c.SystemOneBatch(context.Background(), batchStates(20), batchQuestions(),
		typesafe.WithConcurrency(4))

	err := result.Err()
	if err == nil {
		t.Fatal("Err() = nil with 20 failures")
	}
	msg := err.Error()
	if !strings.Contains(msg, "20 of 20 batch items failed") {
		t.Errorf("summary missing the count: %q", msg)
	}
	// Twenty identical errors read as one grouped line, not twenty.
	if n := strings.Count(msg, "\n") + 1; n > 3 {
		t.Errorf("summary has %d lines, want identical errors grouped:\n%s", n, msg)
	}
}

// --- adaptive concurrency ----------------------------------------------------

// Sustained 429s must shrink the batch-wide limit, and a run of successes must
// let it grow back. Testing the limiter through the public batch API keeps the
// assertion on observable behavior rather than on an internal field.
func TestBatchAdaptiveConcurrencyDecaysAndRecovers(t *testing.T) {
	var (
		mu       sync.Mutex
		inFlight int
		peakLate int // peak once the server has stopped throttling
		calls    int
		throttle = true
	)

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		inFlight++
		n, throttling := inFlight, throttle
		if calls >= 60 {
			throttle = false // stop pushing back; the limit should recover
		}
		if !throttling && n > peakLate {
			peakLate = n
		}
		mu.Unlock()

		time.Sleep(2 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()

		if throttling {
			w.Header().Set("Retry-After", "0")
			writeJSON(t, w, http.StatusTooManyRequests, map[string]any{"detail": "slow down"})
			return
		}
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	result := c.SystemOneBatch(context.Background(), batchStates(400), batchQuestions(),
		typesafe.WithConcurrency(16))

	if result.MinConcurrency >= 16 {
		t.Errorf("MinConcurrency = %d; sustained 429s should have shrunk the limit from 16",
			result.MinConcurrency)
	}
	if result.MinConcurrency < 1 {
		t.Errorf("MinConcurrency = %d; the limit must never reach zero", result.MinConcurrency)
	}
	mu.Lock()
	late := peakLate
	mu.Unlock()
	if late <= result.MinConcurrency {
		t.Errorf("peak concurrency after recovery = %d, never rose above the floor of %d",
			late, result.MinConcurrency)
	}
	if result.Succeeded == 0 {
		t.Error("no item succeeded after the server stopped throttling")
	}
}

// With adaptive concurrency off, the limit is a constant.
func TestBatchConcurrencyCapIsRespected(t *testing.T) {
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()

		time.Sleep(2 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	const limit = 4
	result := c.SystemOneBatch(context.Background(), batchStates(60), batchQuestions(),
		typesafe.WithConcurrency(limit), typesafe.WithAdaptiveConcurrency(false))

	mu.Lock()
	got := peak
	mu.Unlock()
	if got > limit {
		t.Errorf("server saw %d concurrent requests, cap was %d", got, limit)
	}
	if got < 2 {
		t.Errorf("server never saw more than %d at once; the batch did not run concurrently", got)
	}
	if result.PeakConcurrency > limit {
		t.Errorf("PeakConcurrency = %d, cap was %d", result.PeakConcurrency, limit)
	}
	if result.MinConcurrency != limit {
		t.Errorf("MinConcurrency = %d with adaptation off, want %d", result.MinConcurrency, limit)
	}
}

func TestWithConcurrencyClampsToOne(t *testing.T) {
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	c.SystemOneBatch(context.Background(), batchStates(10), batchQuestions(),
		typesafe.WithConcurrency(0))

	mu.Lock()
	defer mu.Unlock()
	if peak != 1 {
		t.Errorf("peak concurrency = %d with WithConcurrency(0), want 1", peak)
	}
}

// --- budget accounting -------------------------------------------------------

// A lifetime cap of k must admit exactly k requests however many workers race
// for them. This is the reservation-under-one-lock property, observed from
// outside: if it were check-then-act, concurrent workers would each see room
// for the last request and the server would see more than k.
func TestBatchBudgetAccountingIsExactUnderConcurrency(t *testing.T) {
	var served atomic.Int64
	const capacity = 25

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		writeJSON(t, w, http.StatusOK, okResponse())
	}, typesafe.WithBudget(typesafe.NewBudget(typesafe.MaxTotalRequests(capacity))))

	result := c.SystemOneBatch(context.Background(), batchStates(200), batchQuestions(),
		typesafe.WithConcurrency(16), typesafe.WithAdaptiveConcurrency(false))

	if got := int(served.Load()); got != capacity {
		t.Errorf("server served %d requests, budget cap was %d", got, capacity)
	}
	if result.Succeeded != capacity {
		t.Errorf("succeeded = %d, want exactly the cap of %d", result.Succeeded, capacity)
	}
	if result.Failed != 200-capacity {
		t.Errorf("failed = %d, want %d", result.Failed, 200-capacity)
	}
	for _, item := range result.Items {
		if item.Err != nil && !errors.Is(item.Err, typesafe.ErrBudgetExceeded) {
			t.Fatalf("item %d failed for an unexpected reason: %v", item.Index, item.Err)
		}
	}
}

// --- cancellation ------------------------------------------------------------

// Canceling mid-batch must still fill every slot: a caller reading Items must
// never find a zero value that reads as a success which never happened.
func TestBatchCancellationFillsEverySlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var served atomic.Int64

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if served.Add(1) == 5 {
			cancel()
		}
		time.Sleep(2 * time.Millisecond)
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	result := c.SystemOneBatch(ctx, batchStates(300), batchQuestions(),
		typesafe.WithConcurrency(2))

	if len(result.Items) != 300 {
		t.Fatalf("got %d items, want 300", len(result.Items))
	}
	for i, item := range result.Items {
		if item.Err == nil && item.Response == nil {
			t.Fatalf("item %d has neither a response nor an error", i)
		}
		if item.State != fmt.Sprintf("state-%d", i) {
			t.Fatalf("item %d carries the wrong state: %v", i, item.State)
		}
	}
	if result.Failed == 0 {
		t.Error("canceling mid-batch produced no failures")
	}
	if int(served.Load()) >= 300 {
		t.Error("cancellation did not stop the batch")
	}
}

// --- the streaming view ------------------------------------------------------

// Every item is yielded exactly once, and the loop terminates.
func TestBatchSeqYieldsEveryItemOnce(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	const n = 200
	seen := make([]int, n)
	count := 0
	for i, item := range c.SystemOneBatchSeq(context.Background(), batchStates(n), batchQuestions(),
		typesafe.WithConcurrency(8)) {
		if i != item.Index {
			t.Fatalf("key %d does not match item.Index %d", i, item.Index)
		}
		if i < 0 || i >= n {
			t.Fatalf("index %d out of range", i)
		}
		seen[i]++
		count++
		if item.Err != nil {
			t.Errorf("item %d: %v", i, item.Err)
		}
	}
	if count != n {
		t.Fatalf("yielded %d items, want %d", count, n)
	}
	for i, c := range seen {
		if c != 1 {
			t.Fatalf("item %d yielded %d times, want exactly 1", i, c)
		}
	}
}

// batchWorkers counts the goroutines currently running inside
// SystemOneBatchSeq, by name.
//
// Counting *all* goroutines would measure the HTTP transport's per-connection
// read and write loops and the test server's handlers, which outlive a batch
// by design and vary with connection reuse. This counts only ours.
func batchWorkers() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "SystemOneBatchSeq")
}

// Breaking out of the range must stop the batch and leave nothing running.
// A leaked goroutine here is the failure mode this shape exists to prevent:
// the obvious implementation leaves every worker blocked on a send into a
// channel nobody will ever read again.
func TestBatchSeqBreakStopsEverything(t *testing.T) {
	var served atomic.Int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		time.Sleep(2 * time.Millisecond)
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	got := 0
	for _, item := range c.SystemOneBatchSeq(context.Background(), batchStates(500), batchQuestions(),
		typesafe.WithConcurrency(8)) {
		_ = item
		got++
		if got == 10 {
			break
		}
	}
	if got != 10 {
		t.Fatalf("consumed %d items before breaking, want 10", got)
	}
	if n := int(served.Load()); n >= 500 {
		t.Errorf("server served %d of 500 after an early break; the batch did not stop", n)
	}

	// The iterator returns only after every worker's send has completed and
	// wg.Wait has returned, so the only wait here is for already-finished
	// goroutines to be scheduled off. A real leak never clears.
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := batchWorkers()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d batch goroutine(s) outlived the call", n)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Nothing still in flight means the server sees no further traffic.
	settled := served.Load()
	time.Sleep(50 * time.Millisecond)
	if after := served.Load(); after != settled {
		t.Errorf("server served %d more requests after the iterator returned", after-settled)
	}
}

// Canceling the context terminates the stream, and every input still reports.
//
// This is the contract SystemOneBatch already keeps, and the streaming view has
// to keep it too: a consumer that is still ranging when the context is
// canceled must be able to tell "canceled after three items" from "canceled
// before anything started". Dropping the remaining sends makes those two
// indistinguishable, and in the worst case yields nothing at all with no error
// anywhere to say why.
func TestBatchSeqTerminatesOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var served atomic.Int64

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if served.Add(1) == 3 {
			cancel()
		}
		time.Sleep(2 * time.Millisecond)
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	const n = 300
	type outcome struct {
		count int
		seen  map[int]int
	}
	done := make(chan outcome, 1)

	go func() {
		res := outcome{seen: map[int]int{}}
		for i, item := range c.SystemOneBatchSeq(ctx, batchStates(n), batchQuestions(),
			typesafe.WithConcurrency(4)) {
			res.count++
			res.seen[i]++
			_ = item
		}
		done <- res
	}()

	select {
	case res := <-done:
		if res.count != n {
			t.Errorf("yielded %d items, want one per input (%d) even under cancellation",
				res.count, n)
		}
		for i := 0; i < n; i++ {
			if res.seen[i] != 1 {
				t.Fatalf("item %d yielded %d times, want exactly 1", i, res.seen[i])
			}
		}
		if int(served.Load()) >= n {
			t.Error("cancellation did not stop new work from starting")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not terminate after cancellation")
	}
}

// A consumer that stops listening is the one case where a result may be
// dropped — nobody is waiting for it.
func TestBatchSeqBreakIsNotCancellation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Millisecond)
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	got := 0
	for range c.SystemOneBatchSeq(context.Background(), batchStates(300), batchQuestions(),
		typesafe.WithConcurrency(4)) {
		got++
		if got == 5 {
			break
		}
	}
	if got != 5 {
		t.Fatalf("consumed %d items, want 5", got)
	}
	// And nothing is left running.
	deadline := time.Now().Add(2 * time.Second)
	for batchWorkers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("batch goroutines outlived the break")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBatchSeqEmpty(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for an empty batch")
	})
	for range c.SystemOneBatchSeq(context.Background(), nil, batchQuestions()) {
		t.Error("empty batch yielded an item")
	}
}

// --- callbacks ---------------------------------------------------------------

func TestBatchItemCallbackFiresOncePerItem(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	var mu sync.Mutex
	seen := map[int]int{}

	const n = 100
	result := c.SystemOneBatch(context.Background(), batchStates(n), batchQuestions(),
		typesafe.WithConcurrency(8),
		typesafe.WithItemCallback(func(item typesafe.ItemResult) {
			mu.Lock()
			defer mu.Unlock()
			seen[item.Index]++
		}))

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != n {
		t.Fatalf("callback saw %d distinct items, want %d", len(seen), n)
	}
	for i := 0; i < n; i++ {
		if seen[i] != 1 {
			t.Errorf("callback for item %d fired %d times", i, seen[i])
		}
	}
	if result.Succeeded != n {
		t.Errorf("succeeded = %d, want %d", result.Succeeded, n)
	}
}

// --- durations ---------------------------------------------------------------

func TestBatchRecordsDurations(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		writeJSON(t, w, http.StatusOK, okResponse())
	})

	result := c.SystemOneBatch(context.Background(), batchStates(4), batchQuestions(),
		typesafe.WithConcurrency(4))

	if result.Duration <= 0 {
		t.Error("BatchResult.Duration was not recorded")
	}
	for i, item := range result.Items {
		if item.Duration <= 0 {
			t.Errorf("item %d has no duration", i)
		}
		if item.Duration > result.Duration {
			t.Errorf("item %d took %s, longer than the whole batch's %s",
				i, item.Duration, result.Duration)
		}
	}
}

// --- model selection ---------------------------------------------------------

// SystemOneBatch builds the request itself, so the model has to come from an
// option; without one the client default would be the only reachable choice.
func TestWithBatchModel(t *testing.T) {
	var mu sync.Mutex
	models := map[string]int{}

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := decodeJSON(r, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		models[body.Model]++
		mu.Unlock()
		writeJSON(t, w, http.StatusOK, okResponse())
	}, typesafe.WithDefaultModel("jev-default"))

	c.SystemOneBatch(context.Background(), batchStates(6), batchQuestions(),
		typesafe.WithConcurrency(3), typesafe.WithBatchModel("jev-1.13.0"))

	mu.Lock()
	defer mu.Unlock()
	if models["jev-1.13.0"] != 6 {
		t.Errorf("model counts = %v, want all 6 as jev-1.13.0", models)
	}
}

func TestBatchUsesClientDefaultModel(t *testing.T) {
	var mu sync.Mutex
	models := map[string]int{}

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := decodeJSON(r, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		models[body.Model]++
		mu.Unlock()
		writeJSON(t, w, http.StatusOK, okResponse())
	}, typesafe.WithDefaultModel("jev-default"))

	c.SystemOneBatch(context.Background(), batchStates(4), batchQuestions())

	mu.Lock()
	defer mu.Unlock()
	if models["jev-default"] != 4 {
		t.Errorf("model counts = %v, want all 4 as jev-default", models)
	}
}
