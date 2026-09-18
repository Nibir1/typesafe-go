package typesafetest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	typesafe "github.com/nibir1/typesafe-go"
)

// TB is the subset of *testing.T this package needs, declared structurally so
// that importing typesafetest does not pull in the testing package.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
	Cleanup(func())
}

// Mock is an in-memory stand-in for a client, satisfying typesafe.API.
//
// Use it when the code under test branches on answers and the transport is
// incidental. When the client's own behavior matters — error mapping, retries,
// validation — use Server or a cassette instead, because a mock skips all of
// it by construction.
//
//	m := typesafetest.NewMock()
//	m.On("is_spam", typesafetest.Noul(0.93))
//	m.On("topic", typesafetest.Choice(map[string]float64{"billing": 0.9, "other": 0.1}))
//
//	verdict, err := classify(ctx, m, ticket)
//
// Mock is safe for concurrent use.
type Mock struct {
	mu sync.Mutex

	answers map[string]any
	queue   []mockReply
	calls   []*typesafe.SystemOneRequest
	models  []typesafe.ModelCard
	modelID string
}

type mockReply struct {
	resp *typesafe.SystemOneResponse
	err  error
}

// NewMock returns an empty mock. With nothing programmed it answers every
// request with an empty answer set, which is enough for code that only checks
// that a call happened.
func NewMock() *Mock {
	return &Mock{
		answers: make(map[string]any),
		modelID: "jev-1.13.0",
		models: []typesafe.ModelCard{
			{Name: "jev-latest", Description: "test model", ReleaseDate: "2026-09-10T00:00:00+00:00"},
		},
	}
}

// On programs a standing answer for a question id. Build the value with Noul,
// Choice, or Score.
func (m *Mock) On(questionID string, answer map[string]any) *Mock {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers[questionID] = answer
	return m
}

// ReturnError queues an error for the next call. Queued outcomes are consumed
// in order and take precedence over standing answers, so a test can script a
// failure followed by a success.
//
//	m.ReturnError(&typesafe.RateLimitError{...}) // first call
//	m.On("q", typesafetest.Noul(0.9))            // every call after
func (m *Mock) ReturnError(err error) *Mock {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, mockReply{err: err})
	return m
}

// ReturnResponse queues a complete response for the next call.
func (m *Mock) ReturnResponse(resp *typesafe.SystemOneResponse) *Mock {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, mockReply{resp: resp})
	return m
}

// SetModels programs what Models returns.
func (m *Mock) SetModels(models ...typesafe.ModelCard) *Mock {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.models = models
	return m
}

// SystemOne implements typesafe.API.
//
// Requests are validated with the same rules the real client applies, so a
// mock does not quietly accept a request the API would reject. That is the one
// piece of real behavior worth keeping: without it, tests pass against
// requests that could never work.
func (m *Mock) SystemOne(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, fmt.Errorf("%w: nil request", typesafe.ErrInvalidRequest)
	}
	if _, err := req.Validate(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)

	if len(m.queue) > 0 {
		next := m.queue[0]
		m.queue = m.queue[1:]
		if next.err != nil {
			return nil, next.err
		}
		if next.resp != nil {
			return next.resp, nil
		}
	}

	answers := make(map[string]json.RawMessage, len(req.Questions))
	for id := range req.Questions {
		a, ok := m.answers[id]
		if !ok {
			// A question with no programmed answer is almost always a test
			// that grew a question and did not grow its mock. Say so, rather
			// than returning a zero value the caller will misread as a real
			// low-confidence answer.
			return nil, fmt.Errorf("typesafetest: no answer programmed for question %q; call Mock.On(%q, ...)", id, id)
		}
		b, err := json.Marshal(a)
		if err != nil {
			return nil, fmt.Errorf("typesafetest: encoding answer %q: %w", id, err)
		}
		answers[id] = b
	}

	return &typesafe.SystemOneResponse{
		Model:   m.modelID,
		Answers: answers,
		Usage:   typesafe.Usage{InputTokens: 100, OutputTokens: 20},
	}, nil
}

// Models implements typesafe.API.
func (m *Mock) Models(ctx context.Context) ([]typesafe.ModelCard, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]typesafe.ModelCard, len(m.models))
	copy(out, m.models)
	return out, nil
}

// Calls returns every request received, in order.
func (m *Mock) Calls() []*typesafe.SystemOneRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*typesafe.SystemOneRequest, len(m.calls))
	copy(out, m.calls)
	return out
}

// CallCount is the number of SystemOne calls received.
func (m *Mock) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// Reset clears recorded calls and queued outcomes, keeping standing answers.
func (m *Mock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
	m.queue = nil
}

// --- transport-level double --------------------------------------------------

// RoundTripFunc adapts a function to http.RoundTripper, for tests that need to
// intercept below the client without running a server.
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// FailingTransport returns an *http.Client whose every request fails with err,
// for exercising the connection-failure path without opening a socket.
func FailingTransport(err error) *http.Client {
	return &http.Client{Transport: RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, err
	})}
}

func readAll(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	b, _ := io.ReadAll(r.Body)
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func pad(n int) string { return itoa(n) }
