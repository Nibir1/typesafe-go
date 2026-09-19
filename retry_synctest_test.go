//go:build go1.25

package typesafe

// Retry timing under testing/synctest, stable since Go 1.25.
//
// The fakeClock tests in retry_test.go prove the client asks for the right
// delays. These prove the delays are actually waited out, using the real clock
// path — inside a synctest bubble, where time is virtual and only advances
// once every goroutine is durably blocked.
//
// That distinction matters. A fake clock can hide a bug where the code
// computes a correct delay and then fails to wait, or waits on the wrong
// channel. Here the production realClock runs unmodified and a 30-second
// budget still completes in microseconds.
//
// Gated at go1.25 so the go 1.23 floor holds; below that, the fakeClock path
// covers the same behaviors.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// bubbleTransport answers requests in memory, with no sockets involved.
//
// This is required, not merely convenient. synctest only advances its virtual
// clock once every goroutine in the bubble is *durably* blocked, and a
// goroutine waiting on a network read is not: the runtime cannot know it will
// stay blocked. Point a client at an httptest.NewServer inside a bubble and it
// hangs until the test binary times out — which is exactly what the first
// draft of this file did.
//
// Answering in memory leaves the backoff timer as the only thing blocking, and
// that is something synctest understands.
type bubbleTransport struct {
	mu      sync.Mutex
	calls   int
	at      []time.Duration
	start   time.Time
	respond func(call int) (int, string)
}

func (b *bubbleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b.mu.Lock()
	b.calls++
	n := b.calls
	b.at = append(b.at, time.Since(b.start))
	respond := b.respond
	b.mu.Unlock()

	status, body := respond(n)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}, nil
}

func (b *bubbleTransport) attempts() []time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]time.Duration, len(b.at))
	copy(out, b.at)
	return out
}

const okBody = `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.5}},` +
	`"usage":{"input_tokens":1,"output_tokens":1}}`

// bubbleClient builds a client whose transport never touches the network.
func bubbleClient(t *testing.T, bt *bubbleTransport, p RetryPolicy) *Client {
	t.Helper()
	bt.start = time.Now()
	c, err := NewClient(
		WithAPIKey("test-key"),
		WithBaseURL("https://api.typesafe.ai"),
		WithHTTPClient(&http.Client{Transport: bt}),
		WithRetryPolicy(p),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestBackoffElapsesRealDurationsUnderSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bt := &bubbleTransport{respond: func(n int) (int, string) {
			if n < 3 {
				return 500, `{"detail":"boom"}`
			}
			return 200, okBody
		}}
		c := bubbleClient(t, bt, noJitter())

		if _, err := c.SystemOne(context.Background(), newRequest()); err != nil {
			t.Fatalf("SystemOne: %v", err)
		}

		at := bt.attempts()
		if len(at) != 3 {
			t.Fatalf("made %d attempts, want 3", len(at))
		}
		// The production realClock actually waits these out; virtual time
		// makes that free.
		if at[1] != 500*time.Millisecond {
			t.Errorf("second attempt at %s, want exactly 500ms", at[1])
		}
		if at[2] != 1500*time.Millisecond {
			t.Errorf("third attempt at %s, want exactly 1.5s", at[2])
		}
	})
}

// TestFullBudgetIsCheapUnderSynctest: the roadmap asks that a test of the 30s
// budget not take 30 seconds. Virtual time makes the assertion honest rather
// than merely fast — the client really does wait, in a timeline that costs
// nothing to advance.
func TestFullBudgetIsCheapUnderSynctest(t *testing.T) {
	wall := time.Now()

	synctest.Test(t, func(t *testing.T) {
		bt := &bubbleTransport{respond: func(int) (int, string) {
			return 503, `{"detail":"unavailable"}`
		}}
		p := noJitter()
		p.MaxRetries = 100
		p.BackoffInitial = time.Second
		p.BackoffMax = 5 * time.Second
		p.Timeout = 30 * time.Second

		c := bubbleClient(t, bt, p)

		start := time.Now()
		_, err := c.SystemOne(context.Background(), newRequest())
		virtual := time.Since(start)

		if err == nil {
			t.Fatal("expected failure")
		}
		if !errors.Is(err, ErrRetriesExhausted) {
			t.Errorf("err = %v, want it to match ErrRetriesExhausted", err)
		}
		if virtual > 30*time.Second {
			t.Errorf("spent %s of virtual time, above the 30s budget", virtual)
		}
		if virtual < 20*time.Second {
			t.Errorf("spent only %s; the budget should have been used", virtual)
		}
		if n := len(bt.attempts()); n < 8 {
			t.Errorf("made only %d attempts within a 30s budget", n)
		}
	})

	if elapsed := time.Since(wall); elapsed > 2*time.Second {
		t.Errorf("took %s of real time to simulate a 30s budget", elapsed)
	}
}

func TestCancellationIsImmediateUnderSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bt := &bubbleTransport{respond: func(int) (int, string) {
			return 500, `{"detail":"boom"}`
		}}
		p := noJitter()
		p.BackoffInitial = time.Hour
		p.Timeout = 0
		c := bubbleClient(t, bt, p)

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(time.Second)
			cancel()
		}()

		start := time.Now()
		_, err := c.SystemOne(ctx, newRequest())
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected failure")
		}
		// Canceled one virtual second in, not an hour.
		if elapsed > 2*time.Second {
			t.Errorf("returned after %s; cancellation should abort the hour-long backoff at once", elapsed)
		}
	})
}
