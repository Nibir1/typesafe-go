package nethttp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/integrations/nethttp"
)

func newClient(t *testing.T, seen *string) *typesafe.Client {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Get("x-correlation-id")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "jev-1.13.0",
			"answers": map[string]any{"q": map[string]any{"type": "noul", "noul": 0.9}},
			"usage":   map[string]any{"input_tokens": 10, "output_tokens": 1},
		})
	}))
	t.Cleanup(api.Close)

	c, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(api.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		// Send the id, so the test can assert it reached the wire rather than
		// only the context.
		typesafe.WithRequestID(func() string { return "generated-id" }, "x-correlation-id"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestMiddlewareInjectsTheClient(t *testing.T) {
	client := newClient(t, nil)

	var got *typesafe.Client
	h := nethttp.Middleware(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nethttp.MustFrom(r.Context())
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if got != client {
		t.Error("the handler did not receive the client that was installed")
	}
}

func TestFromWithoutMiddleware(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := nethttp.From(r.Context()); ok {
		t.Error("From reported a client that was never installed")
	}

	defer func() {
		if recover() == nil {
			t.Error("MustFrom should panic when there is no client")
		}
	}()
	nethttp.MustFrom(r.Context())
}

// The point of the package: the inbound correlation id must reach the API, so
// one id ties the request, the SDK's logs and the API's records together.
func TestCorrelationIDReachesTheAPI(t *testing.T) {
	var sent string
	client := newClient(t, &sent)

	h := nethttp.CorrelationMiddleware()(
		nethttp.Middleware(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := nethttp.MustFrom(r.Context())
			if _, err := c.SystemOne(r.Context(), &typesafe.SystemOneRequest{
				State:     "x",
				Questions: typesafe.Questions{"q": typesafe.Noul{Instructions: "?"}},
			}); err != nil {
				t.Errorf("SystemOne: %v", err)
			}

			if id, ok := nethttp.CorrelationID(r.Context()); !ok || id != "inbound-123" {
				t.Errorf("CorrelationID = %q, %v; want inbound-123", id, ok)
			}
		})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "inbound-123")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if sent != "inbound-123" {
		t.Errorf("the API saw correlation id %q, want the inbound inbound-123", sent)
	}
}

// Without an inbound header the client's own generator must still apply —
// propagation must not blank the id out.
func TestNoInboundHeaderFallsBackToTheGenerator(t *testing.T) {
	var sent string
	client := newClient(t, &sent)

	h := nethttp.CorrelationMiddleware()(
		nethttp.Middleware(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := nethttp.MustFrom(r.Context())
			if _, err := c.SystemOne(r.Context(), &typesafe.SystemOneRequest{
				State:     "x",
				Questions: typesafe.Questions{"q": typesafe.Noul{Instructions: "?"}},
			}); err != nil {
				t.Errorf("SystemOne: %v", err)
			}
		})))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if sent != "generated-id" {
		t.Errorf("the API saw %q, want the client's generated id", sent)
	}
}

// Headers are checked in order of specificity: an id a proxy set for this hop
// beats the trace-wide traceparent.
func TestHeaderPrecedence(t *testing.T) {
	var got string
	h := nethttp.CorrelationMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = nethttp.CorrelationID(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("traceparent", "00-trace-span-01")
	req.Header.Set("X-Correlation-Id", "corr")
	req.Header.Set("X-Request-Id", "reqid")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != "reqid" {
		t.Errorf("CorrelationID = %q, want the X-Request-Id value", got)
	}
}

func TestCustomHeader(t *testing.T) {
	var got string
	h := nethttp.CorrelationMiddleware("X-My-Id")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = nethttp.CorrelationID(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "ignored")
	req.Header.Set("X-My-Id", "mine")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != "mine" {
		t.Errorf("CorrelationID = %q, want mine", got)
	}
}
