package echo_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	typesafe "github.com/nibir1/typesafe-go"
	tsecho "github.com/nibir1/typesafe-go/integrations/echo"
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
		typesafe.WithRequestID(func() string { return "generated-id" }, "x-correlation-id"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func sample() *typesafe.SystemOneRequest {
	return &typesafe.SystemOneRequest{
		State:     "x",
		Questions: typesafe.Questions{"q": typesafe.Noul{Instructions: "?"}},
	}
}

func TestMiddlewareAndCorrelation(t *testing.T) {
	var sent string
	client := newClient(t, &sent)

	e := echo.New()
	e.Use(tsecho.Middleware(client), tsecho.Correlation())
	e.GET("/", func(c echo.Context) error {
		ts := tsecho.MustFrom(c)
		if ts != client {
			t.Error("the handler received a different client")
		}
		if _, err := ts.SystemOne(c.Request().Context(), sample()); err != nil {
			t.Errorf("SystemOne: %v", err)
		}
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "inbound-123")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	if sent != "inbound-123" {
		t.Errorf("the API saw correlation id %q, want inbound-123", sent)
	}
}

func TestNoInboundHeaderKeepsTheGeneratedID(t *testing.T) {
	var sent string
	client := newClient(t, &sent)

	e := echo.New()
	e.Use(tsecho.Middleware(client), tsecho.Correlation())
	e.GET("/", func(c echo.Context) error {
		if _, err := tsecho.MustFrom(c).SystemOne(c.Request().Context(), sample()); err != nil {
			t.Errorf("SystemOne: %v", err)
		}
		return c.NoContent(http.StatusNoContent)
	})

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if sent != "generated-id" {
		t.Errorf("the API saw %q, want the client's generated id", sent)
	}
}

func TestFromWithoutMiddleware(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())

	if _, ok := tsecho.From(c); ok {
		t.Error("From reported a client that was never installed")
	}

	defer func() {
		if recover() == nil {
			t.Error("MustFrom should panic when Middleware is missing")
		}
	}()
	tsecho.MustFrom(c)
}
