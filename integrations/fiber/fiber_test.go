package fiber_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	typesafe "github.com/nibir1/typesafe-go"
	tsfiber "github.com/nibir1/typesafe-go/integrations/fiber"
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

	app := fiber.New()
	app.Use(tsfiber.Middleware(client), tsfiber.Correlation())
	app.Get("/", func(c fiber.Ctx) error {
		ts := tsfiber.MustFrom(c)
		if ts != client {
			t.Error("the handler received a different client")
		}
		// c.Context(), not context.Background(): Fiber's context is what
		// carries the client and the correlation id, and what is canceled
		// when the caller hangs up.
		if _, err := ts.SystemOne(c.Context(), sample()); err != nil {
			t.Errorf("SystemOne: %v", err)
		}
		return c.SendStatus(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "inbound-123")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if sent != "inbound-123" {
		t.Errorf("the API saw correlation id %q, want inbound-123", sent)
	}
}

func TestNoInboundHeaderKeepsTheGeneratedID(t *testing.T) {
	var sent string
	client := newClient(t, &sent)

	app := fiber.New()
	app.Use(tsfiber.Middleware(client), tsfiber.Correlation())
	app.Get("/", func(c fiber.Ctx) error {
		if _, err := tsfiber.MustFrom(c).SystemOne(c.Context(), sample()); err != nil {
			t.Errorf("SystemOne: %v", err)
		}
		return c.SendStatus(http.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()

	if sent != "generated-id" {
		t.Errorf("the API saw %q, want the client's generated id", sent)
	}
}

func TestMustFromPanicsWithoutMiddleware(t *testing.T) {
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error {
		if _, ok := tsfiber.From(c); ok {
			t.Error("From reported a client that was never installed")
		}
		defer func() {
			if recover() == nil {
				t.Error("MustFrom should panic when Middleware is missing")
			}
		}()
		tsfiber.MustFrom(c)
		return nil
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
}
