package gin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	typesafe "github.com/nibir1/typesafe-go"
	tsgin "github.com/nibir1/typesafe-go/integrations/gin"
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

func init() { gin.SetMode(gin.TestMode) }

func TestMiddlewareAndCorrelation(t *testing.T) {
	var sent string
	client := newClient(t, &sent)

	r := gin.New()
	r.Use(tsgin.Middleware(client), tsgin.Correlation())
	r.GET("/", func(c *gin.Context) {
		ts := tsgin.MustFrom(c)
		if ts != client {
			t.Error("the handler received a different client")
		}
		// The context passed here is the request's, which is what carries
		// both the client and the correlation id.
		if _, err := ts.SystemOne(c.Request.Context(), sample()); err != nil {
			t.Errorf("SystemOne: %v", err)
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "inbound-123")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

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

	r := gin.New()
	r.Use(tsgin.Middleware(client), tsgin.Correlation())
	r.GET("/", func(c *gin.Context) {
		if _, err := tsgin.MustFrom(c).SystemOne(c.Request.Context(), sample()); err != nil {
			t.Errorf("SystemOne: %v", err)
		}
		c.Status(http.StatusNoContent)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if sent != "generated-id" {
		t.Errorf("the API saw %q, want the client's generated id", sent)
	}
}

func TestMustFromPanicsWithoutMiddleware(t *testing.T) {
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		defer func() {
			if recover() == nil {
				t.Error("MustFrom should panic when Middleware is missing")
			}
			c.Status(http.StatusNoContent)
		}()
		tsgin.MustFrom(c)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestFromReportsAbsence(t *testing.T) {
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		if _, ok := tsgin.From(c); ok {
			t.Error("From reported a client that was never installed")
		}
		c.Status(http.StatusNoContent)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}
