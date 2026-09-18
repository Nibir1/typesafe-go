// Package typesafetest provides doubles for testing code that calls the
// TypeSafe API: a programmable HTTP server, canned responses for every
// documented failure, and assertion helpers.
//
// Two levels are available, and they test different things.
//
// Server runs a real httptest server, so the SDK's own marshalling, status
// mapping, and error typing all execute. Use it when the behavior under test
// involves the client.
//
// Mock replaces the client entirely and satisfies typesafe.API. Use it when
// the behavior under test is your own code's branching on answers, and the
// client is incidental.
//
// For real recorded traffic, see the cassette package.
package typesafetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"

	typesafe "github.com/nibir1/typesafe-go"
)

// Server is a programmable stand-in for the TypeSafe API.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	handlers []http.HandlerFunc
	requests []RecordedRequest
	calls    int
}

// RecordedRequest is one request the server received, for assertions.
type RecordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// Request decodes the body as a System One request.
func (r RecordedRequest) Request() (map[string]any, error) {
	var out map[string]any
	err := json.Unmarshal(r.Body, &out)
	return out, err
}

// NewServer starts a server that serves the given responses in order.
//
// The final response repeats once the list is exhausted, so a test that does
// not care how many calls happen does not have to count them. With no
// responses at all, every call gets a valid empty-answers 200.
//
//	srv := typesafetest.NewServer(t,
//	    typesafetest.RateLimited(2*time.Second),  // first call
//	    typesafetest.Answers(map[string]any{...}), // thereafter
//	)
//	client, _ := typesafe.NewClient(
//	    typesafe.WithAPIKey("test"),
//	    typesafe.WithBaseURL(srv.URL),
//	)
func NewServer(tb TB, responses ...http.HandlerFunc) *Server {
	tb.Helper()
	s := &Server{handlers: responses}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	tb.Cleanup(s.Close)
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body := readAll(r)

	s.mu.Lock()
	s.requests = append(s.requests, RecordedRequest{
		Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body,
	})
	n := s.calls
	s.calls++
	var h http.HandlerFunc
	switch {
	case len(s.handlers) == 0:
		h = Answers(nil)
	case n < len(s.handlers):
		h = s.handlers[n]
	default:
		h = s.handlers[len(s.handlers)-1]
	}
	s.mu.Unlock()

	w.Header().Set(typesafe.RequestIDHeader, "req_test_"+pad(n))
	h(w, r)
}

// Calls is the number of requests received.
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Requests returns every request received, in order.
func (s *Server) Requests() []RecordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecordedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// LastRequest returns the most recent request, and whether there was one.
func (s *Server) LastRequest() (RecordedRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return RecordedRequest{}, false
	}
	return s.requests[len(s.requests)-1], true
}

// --- canned responses --------------------------------------------------------

// Answers returns a 200 carrying the given answers, keyed by question id.
//
// Build the values with Noul, Choice, and Score.
func Answers(answers map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if answers == nil {
			answers = map[string]any{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model":   "jev-1.13.0",
			"answers": answers,
			"usage":   map[string]any{"input_tokens": 100, "output_tokens": 20},
		})
	}
}

// Noul builds a noul answer value for Answers.
func Noul(p float64) map[string]any {
	return map[string]any{"type": "noul", "noul": p}
}

// Choice builds a choice answer value for Answers. The winning option and the
// confidence are derived from the distribution, so the result is internally
// consistent without the caller having to keep three numbers in agreement.
func Choice(probabilities map[string]float64) map[string]any {
	best, bestP := "", -1.0
	for opt, p := range probabilities {
		if p > bestP || (p == bestP && opt < best) {
			best, bestP = opt, p
		}
	}
	probs := make(map[string]any, len(probabilities))
	for k, v := range probabilities {
		probs[k] = v
	}
	return map[string]any{
		"type": "choice", "choice": best,
		"probabilities": probs, "confidence": bestP,
	}
}

// Score builds a score answer value for Answers. The score is the
// probability-weighted mean of the level indices, matching what the API
// returns, so an answer built here satisfies the same invariants a real one
// does.
func Score(levels []string, probabilities []float64) map[string]any {
	legend := make(map[string]any, len(levels))
	probs := make(map[string]any, len(probabilities))
	var weighted, best float64
	for i, l := range levels {
		legend[itoa(i)] = l
		var p float64
		if i < len(probabilities) {
			p = probabilities[i]
		}
		probs[itoa(i)] = p
		weighted += float64(i) * p
		if p > best {
			best = p
		}
	}
	return map[string]any{
		"type": "score", "score": weighted,
		"legend": legend, "probabilities": probs, "confidence": best,
	}
}

// Status returns a handler replying with a bare status code.
func Status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }
}

// Unauthorized returns the 401 the live API sends, in its object detail shape.
func Unauthorized() http.HandlerFunc {
	return errorDetail(http.StatusUnauthorized, "authentication_error",
		"Cannot authenticate with the server. Please check your API key and try again.")
}

// BadRequest returns a 400 in the object detail shape, as the live API sends
// for an unknown model or an oversized Score rubric.
func BadRequest(message string) http.HandlerFunc {
	return errorDetail(http.StatusBadRequest, "api_usage_error", message)
}

// Overloaded returns the 529 the API sends when saturated.
func Overloaded() http.HandlerFunc {
	return errorDetail(529, "overloaded_error", "TypeSafe is temporarily overloaded.")
}

// Unprocessable returns a 422 in the array detail shape, naming the field that
// failed, exactly as the live API does.
//
//	typesafetest.Unprocessable([]string{"body", "questions", "q", "choice", "criteria"},
//	    "Field required", "missing")
func Unprocessable(loc []string, msg, kind string) http.HandlerFunc {
	path := make([]any, len(loc))
	for i, p := range loc {
		path[i] = p
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"detail": []map[string]any{{"loc": path, "msg": msg, "type": kind}},
		})
	}
}

// RateLimited returns a 429 with a retry-after of the given duration in
// seconds. Pass 0 to omit the header, which the API is not guaranteed to send.
func RateLimited(retryAfterSeconds int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if retryAfterSeconds > 0 {
			w.Header().Set("Retry-After", itoa(retryAfterSeconds))
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"detail": map[string]any{
				"error_type": "rate_limit_error",
				"message":    "You have exceeded your rate limit.",
			},
		})
	}
}

// Malformed returns a 200 whose body is not the documented shape, for
// exercising the response-validation path.
func Malformed(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

// Models returns a 200 for GET /v1/models.
func Models(names ...string) http.HandlerFunc {
	if len(names) == 0 {
		names = []string{"jev-latest", "jev-preview"}
	}
	list := make([]map[string]string, 0, len(names))
	for _, n := range names {
		list = append(list, map[string]string{
			"name": n, "description": "test model", "release_date": "2026-09-10T00:00:00+00:00",
		})
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"models": list})
	}
}

func errorDetail(status int, kind, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, status, map[string]any{
			"detail": map[string]any{"error_type": kind, "message": message},
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
