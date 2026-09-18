package typesafetest_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/typesafetest"
)

func serverClient(t *testing.T, responses ...http.HandlerFunc) (*typesafe.Client, *typesafetest.Server) {
	t.Helper()
	srv := typesafetest.NewServer(t, responses...)
	c, err := typesafe.NewClient(typesafe.WithAPIKey("test"), typesafe.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

func oneNoul() *typesafe.SystemOneRequest {
	return &typesafe.SystemOneRequest{
		State:     "x",
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{Instructions: "?"}},
	}
}

// TestServerProducesEveryDocumentedError: the roadmap's exit criterion. Every
// failure the API is known to return must be reachable from a double, or tests
// cannot cover the error paths at all.
func TestServerProducesEveryDocumentedError(t *testing.T) {
	cases := []struct {
		name     string
		handler  http.HandlerFunc
		status   int
		sentinel error
	}{
		{"400", typesafetest.BadRequest("Unknown model: nope"), 400, typesafe.ErrBadRequest},
		{"401", typesafetest.Unauthorized(), 401, typesafe.ErrAuthentication},
		{"403", typesafetest.Status(403), 403, typesafe.ErrPermissionDenied},
		{"404", typesafetest.Status(404), 404, typesafe.ErrNotFound},
		{"422", typesafetest.Unprocessable([]string{"body", "state"}, "Field required", "missing"),
			422, typesafe.ErrInvalidRequest},
		{"429", typesafetest.RateLimited(3), 429, typesafe.ErrRateLimit},
		{"500", typesafetest.Status(500), 500, typesafe.ErrInternalServer},
		{"529", typesafetest.Overloaded(), 529, typesafe.ErrOverloaded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := serverClient(t, tc.handler)
			_, err := c.SystemOne(context.Background(), oneNoul())
			typesafetest.AssertErrorIs(t, err, tc.sentinel)
			typesafetest.AssertAPIStatus(t, err, tc.status)
		})
	}

	t.Run("malformed response", func(t *testing.T) {
		c, _ := serverClient(t, typesafetest.Malformed("<html>nope</html>"))
		_, err := c.SystemOne(context.Background(), oneNoul())
		typesafetest.AssertErrorIs(t, err, typesafe.ErrInvalidResponse)
	})

	t.Run("connection failure", func(t *testing.T) {
		c, err := typesafe.NewClient(
			typesafe.WithAPIKey("test"),
			typesafe.WithHTTPClient(typesafetest.FailingTransport(errors.New("dial refused"))),
		)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		_, err = c.SystemOne(context.Background(), oneNoul())
		typesafetest.AssertErrorIs(t, err, typesafe.ErrConnection)
	})
}

// TestRateLimitedCarriesRetryAfter: Phase 4 will lean on this, so the double
// has to produce a header the client actually parses.
func TestRateLimitedCarriesRetryAfter(t *testing.T) {
	c, _ := serverClient(t, typesafetest.RateLimited(7))
	_, err := c.SystemOne(context.Background(), oneNoul())

	var rl *typesafe.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %T, want *RateLimitError", err)
	}
	if rl.RetryAfter.Seconds() != 7 {
		t.Errorf("RetryAfter = %v, want 7s", rl.RetryAfter)
	}

	// Zero must omit the header, since the API is not guaranteed to send one.
	c2, _ := serverClient(t, typesafetest.RateLimited(0))
	_, err = c2.SystemOne(context.Background(), oneNoul())
	if !errors.As(err, &rl) {
		t.Fatalf("err = %T", err)
	}
	if rl.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0 when the header is absent", rl.RetryAfter)
	}
}

// TestServerAdvancesThroughResponses: scripting a failure then a success is
// what Phase 4's retry tests will need.
func TestServerAdvancesThroughResponses(t *testing.T) {
	c, srv := serverClient(t,
		typesafetest.Status(500),
		typesafetest.Status(500),
		typesafetest.Answers(map[string]any{"q": typesafetest.Noul(0.8)}),
	)
	for i := range 2 {
		if _, err := c.SystemOne(context.Background(), oneNoul()); err == nil {
			t.Fatalf("call %d should have failed", i)
		}
	}
	resp, err := c.SystemOne(context.Background(), oneNoul())
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	typesafetest.AssertNoulAbove(t, resp, "q", 0.7)

	// The last handler repeats, so a test need not count calls exactly.
	if _, err := c.SystemOne(context.Background(), oneNoul()); err != nil {
		t.Fatalf("fourth call: %v", err)
	}
	if got := srv.Calls(); got != 4 {
		t.Errorf("Calls = %d, want 4", got)
	}
}

// TestServerRecordsRequests: assertions about what was sent are as important
// as assertions about what came back.
func TestServerRecordsRequests(t *testing.T) {
	c, srv := serverClient(t, typesafetest.Answers(map[string]any{"q": typesafetest.Noul(0.5)}))
	if _, err := c.SystemOne(context.Background(), oneNoul()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	last, ok := srv.LastRequest()
	if !ok {
		t.Fatal("no request recorded")
	}
	if last.Method != http.MethodPost || last.Path != typesafe.SystemOnePath {
		t.Errorf("got %s %s", last.Method, last.Path)
	}
	body, err := last.Request()
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["model"] != typesafe.DefaultModel {
		t.Errorf("model = %v, want the default to have been applied", body["model"])
	}
	if last.Header.Get("Authorization") == "" {
		t.Error("the request carried no Authorization header")
	}
}

// TestBuiltAnswersAreInternallyConsistent: a fixture whose numbers disagree
// makes thresholds behave in ways that look like a bug in the code under test,
// so the builders derive the dependent values rather than trusting the caller.
func TestBuiltAnswersAreInternallyConsistent(t *testing.T) {
	c, _ := serverClient(t, typesafetest.Answers(map[string]any{
		"choice": typesafetest.Choice(map[string]float64{"a": 0.7, "b": 0.2, "c": 0.1}),
		"score":  typesafetest.Score([]string{"lo", "mid", "hi"}, []float64{0.1, 0.3, 0.6}),
	}))
	req := &typesafe.SystemOneRequest{
		State: "x",
		Questions: map[string]typesafe.Question{
			"choice": typesafe.Choice{Criteria: typesafe.Options{"a": nil, "b": nil, "c": nil}},
			"score":  typesafe.Score{Criteria: typesafe.Levels{"lo", "mid", "hi"}},
		},
	}
	resp, err := c.SystemOne(context.Background(), req)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	typesafetest.AssertProbabilitiesSumToOne(t, resp)
	typesafetest.AssertChoice(t, resp, "choice", "a")

	// 0*0.1 + 1*0.3 + 2*0.6 = 1.5, the same rule the live API follows.
	typesafetest.AssertScoreBetween(t, resp, "score", 1.49, 1.51)
}

// --- mock --------------------------------------------------------------------

func TestMockSatisfiesAPI(t *testing.T) {
	var _ typesafe.API = typesafetest.NewMock()
}

func TestMockAnswers(t *testing.T) {
	m := typesafetest.NewMock().
		On("is_spam", typesafetest.Noul(0.93)).
		On("topic", typesafetest.Choice(map[string]float64{"billing": 0.9, "other": 0.1}))

	resp, err := m.SystemOne(context.Background(), &typesafe.SystemOneRequest{
		State: "buy now",
		Questions: map[string]typesafe.Question{
			"is_spam": typesafe.Noul{Instructions: "spam?"},
			"topic":   typesafe.Choice{Criteria: typesafe.Options{"billing": nil, "other": nil}},
		},
	})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	typesafetest.AssertNoulAbove(t, resp, "is_spam", 0.9)
	typesafetest.AssertChoice(t, resp, "topic", "billing")
	if m.CallCount() != 1 {
		t.Errorf("CallCount = %d", m.CallCount())
	}
}

// TestMockValidatesLikeTheRealClient: a mock that accepts an illegal request
// lets a test pass against something that could never work in production.
func TestMockValidatesLikeTheRealClient(t *testing.T) {
	m := typesafetest.NewMock()

	levels := make(typesafe.Levels, typesafe.MaxScoreLevels+1)
	for i := range levels {
		levels[i] = "l"
	}
	_, err := m.SystemOne(context.Background(), &typesafe.SystemOneRequest{
		State:     "x",
		Questions: map[string]typesafe.Question{"q": typesafe.Score{Criteria: levels}},
	})
	typesafetest.AssertErrorIs(t, err, typesafe.ErrInvalidRequest)

	_, err = m.SystemOne(context.Background(), &typesafe.SystemOneRequest{
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{}},
	})
	typesafetest.AssertErrorIs(t, err, typesafe.ErrInvalidRequest)
}

// TestMockReportsUnprogrammedQuestions: returning a zero value would read as a
// real low-confidence answer, which is the sort of thing that wastes an
// afternoon.
func TestMockReportsUnprogrammedQuestions(t *testing.T) {
	m := typesafetest.NewMock()
	_, err := m.SystemOne(context.Background(), oneNoul())
	if err == nil {
		t.Fatal("expected an error for an unprogrammed question")
	}
	if got := err.Error(); !contains(got, "Mock.On") {
		t.Errorf("the error should say how to fix it, got: %v", got)
	}
}

func TestMockQueuedOutcomes(t *testing.T) {
	m := typesafetest.NewMock().On("q", typesafetest.Noul(0.6))
	m.ReturnError(errors.New("boom"))

	if _, err := m.SystemOne(context.Background(), oneNoul()); err == nil {
		t.Fatal("queued error was not returned")
	}
	resp, err := m.SystemOne(context.Background(), oneNoul())
	if err != nil {
		t.Fatalf("after the queue drained: %v", err)
	}
	typesafetest.AssertNoulAbove(t, resp, "q", 0.5)
	if m.CallCount() != 2 {
		t.Errorf("CallCount = %d, want 2", m.CallCount())
	}
}

func TestMockRespectsContext(t *testing.T) {
	m := typesafetest.NewMock().On("q", typesafetest.Noul(0.5))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.SystemOne(ctx, oneNoul()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, err := m.Models(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Models err = %v, want context.Canceled", err)
	}
}

// TestAssertConfidenceOnNoulExplainsItself: asserting confidence on a Noul is
// a category error, and the message should say so rather than comparing
// against a zero.
func TestAssertConfidenceOnNoulExplainsItself(t *testing.T) {
	c, _ := serverClient(t, typesafetest.Answers(map[string]any{"q": typesafetest.Noul(0.99)}))
	resp, err := c.SystemOne(context.Background(), oneNoul())
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	fake := &recordingTB{}
	typesafetest.AssertConfidenceAtLeast(fake, resp, "q", 0.5)
	if len(fake.errs) != 1 {
		t.Fatalf("expected one reported error, got %v", fake.errs)
	}
	if !contains(fake.errs[0], "noul answers carry none") {
		t.Errorf("message should explain why, got: %q", fake.errs[0])
	}
}

type recordingTB struct{ errs []string }

func (r *recordingTB) Helper()                   {}
func (r *recordingTB) Fatalf(string, ...any)     {}
func (r *recordingTB) Errorf(f string, a ...any) { r.errs = append(r.errs, sprintf(f, a...)) }
func (r *recordingTB) Cleanup(func())            {}

func contains(h, n string) bool         { return strings.Contains(h, n) }
func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func TestModelsDouble(t *testing.T) {
	c, _ := serverClient(t, typesafetest.Models("jev-latest", "jev-preview"))
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0].Name != "jev-latest" {
		t.Errorf("models = %+v", models)
	}
}
