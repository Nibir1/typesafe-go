package temporal_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
	tstemporal "github.com/nibir1/typesafe-go/integrations/temporal"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// --- harness -----------------------------------------------------------------

func apiServer(t *testing.T, status int, body map[string]any, calls *int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			*calls++
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func okBody() map[string]any {
	return map[string]any{
		"model": "jev-1.13.0",
		"answers": map[string]any{
			"is_urgent": map[string]any{"type": "noul", "noul": 0.92},
			"team": map[string]any{
				"type": "choice", "choice": "billing", "confidence": 0.81,
				"probabilities": map[string]float64{"billing": 0.8, "technical": 0.2},
			},
		},
		"usage": map[string]any{"input_tokens": 312, "output_tokens": 48},
	}
}

func tsClient(t *testing.T, url string) *typesafe.Client {
	t.Helper()
	c, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(url),
		// Temporal owns the retries; the SDK must not add a second layer.
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func questions() typesafe.Questions {
	return typesafe.Questions{
		"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
		"team": typesafe.Choice{
			Instructions: "Which team?",
			Criteria:     typesafe.Options{"billing": "money", "technical": "bugs"},
		},
	}
}

// --- serialization -----------------------------------------------------------

// The constraint that shapes the whole input type: Temporal deserializes the
// activity input on the worker side, and typesafe.Question is a sealed
// interface with no concrete type to decode into.
func TestInputRoundTripsThroughJSON(t *testing.T) {
	in, err := tstemporal.NewInput("a ticket", questions())
	if err != nil {
		t.Fatalf("NewInput: %v", err)
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back tstemporal.SystemOneInput
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.State != "a ticket" {
		t.Errorf("state = %v", back.State)
	}
	if len(back.Questions) != 2 {
		t.Fatalf("%d questions survived the round trip, want 2", len(back.Questions))
	}
	// The wire discriminator must survive, or the API cannot dispatch.
	if got := back.Questions["is_urgent"]["type"]; got != "noul" {
		t.Errorf("is_urgent type = %v, want noul", got)
	}
	if got := back.Questions["team"]["type"]; got != "choice" {
		t.Errorf("team type = %v, want choice", got)
	}

	// And the converted questions still marshal to the same bytes the typed
	// forms would, so going through Temporal changes nothing on the wire.
	direct, err := json.Marshal(questions()["team"])
	if err != nil {
		t.Fatalf("marshal typed: %v", err)
	}
	viaTemporal, err := json.Marshal(back.Questions["team"])
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	var a, c any
	_ = json.Unmarshal(direct, &a)
	_ = json.Unmarshal(viaTemporal, &c)
	if !reflect.DeepEqual(a, c) {
		t.Errorf("the round trip changed the wire form:\n typed: %s\n via:   %s", direct, viaTemporal)
	}
}

func TestWithModel(t *testing.T) {
	in, _ := tstemporal.NewInput("x", questions())
	if got := in.WithModel("jev-1.13.0").Model; got != "jev-1.13.0" {
		t.Errorf("Model = %q", got)
	}
	if in.Model != "" {
		t.Error("WithModel mutated the original")
	}
}

// --- the activity, in Temporal's activity environment ------------------------

func TestSystemOneActivity(t *testing.T) {
	var calls int
	url := apiServer(t, http.StatusOK, okBody(), &calls)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	acts := tstemporal.NewActivities(tsClient(t, url))
	tstemporal.Register(env, acts)

	in, err := tstemporal.NewInput("My payouts are failing.", questions())
	if err != nil {
		t.Fatalf("NewInput: %v", err)
	}

	val, err := env.ExecuteActivity(tstemporal.SystemOneActivityName, in)
	if err != nil {
		t.Fatalf("ExecuteActivity: %v", err)
	}

	var resp typesafe.SystemOneResponse
	if err := val.Get(&resp); err != nil {
		t.Fatalf("decoding the activity result: %v", err)
	}
	if resp.Model != "jev-1.13.0" {
		t.Errorf("model = %q", resp.Model)
	}
	if calls != 1 {
		t.Errorf("the API saw %d calls, want 1", calls)
	}

	// The typed accessors survive the trip through Temporal's data converter,
	// which is the point of returning the SDK's own response type.
	team, err := resp.Choice("team")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if team.Choice != "billing" || team.Confidence != 0.81 {
		t.Errorf("team = %q confidence = %v", team.Choice, team.Confidence)
	}
}

// A failure that a retry cannot fix must be non-retryable, or Temporal burns
// the whole policy on a request that can never work.
func TestUnfixableFailuresAreNonRetryable(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   map[string]any
		typ    string
	}{
		{"bad key", http.StatusUnauthorized, map[string]any{"detail": "bad key"}, "Authentication"},
		{"forbidden", http.StatusForbidden, map[string]any{"detail": "no"}, "PermissionDenied"},
		{"unprocessable", http.StatusUnprocessableEntity, map[string]any{"detail": "bad shape"}, "InvalidRequest"},
		{"not found", http.StatusNotFound, map[string]any{"detail": "nope"}, "NotFound"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := apiServer(t, tc.status, tc.body, nil)

			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			tstemporal.Register(env, tstemporal.NewActivities(tsClient(t, url)))

			in, _ := tstemporal.NewInput("x", questions())
			_, err := env.ExecuteActivity(tstemporal.SystemOneActivityName, in)
			if err == nil {
				t.Fatal("expected a failure")
			}

			var appErr *temporal.ApplicationError
			if !errors.As(err, &appErr) {
				t.Fatalf("error is %T, want *temporal.ApplicationError: %v", err, err)
			}
			if !appErr.NonRetryable() {
				t.Errorf("%s should be non-retryable", tc.name)
			}
			if appErr.Type() != tc.typ {
				t.Errorf("error type = %q, want %q", appErr.Type(), tc.typ)
			}
		})
	}
}

// A 500 is exactly what Temporal's retry policy is for, so it must stay
// retryable.
func TestTransientFailuresStayRetryable(t *testing.T) {
	url := apiServer(t, http.StatusInternalServerError, map[string]any{"detail": "boom"}, nil)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	tstemporal.Register(env, tstemporal.NewActivities(tsClient(t, url)))

	in, _ := tstemporal.NewInput("x", questions())
	_, err := env.ExecuteActivity(tstemporal.SystemOneActivityName, in)
	if err == nil {
		t.Fatal("expected a failure")
	}

	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.NonRetryable() {
		t.Error("a 500 must stay retryable")
	}
}

func TestActivityWithoutAClientFailsNonRetryably(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	tstemporal.Register(env, tstemporal.NewActivities(nil))

	in, _ := tstemporal.NewInput("x", questions())
	_, err := env.ExecuteActivity(tstemporal.SystemOneActivityName, in)
	if err == nil {
		t.Fatal("expected a failure")
	}
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) || !appErr.NonRetryable() {
		t.Errorf("a misconfigured worker must fail non-retryably, got %v", err)
	}
}

// --- the workflow side -------------------------------------------------------

// triageWorkflow is the shape the package documentation prescribes: no network
// call in workflow code, only an Activity.
func triageWorkflow(ctx workflow.Context, ticket string) (string, error) {
	in, err := tstemporal.NewInput(ticket, questions())
	if err != nil {
		return "", err
	}
	resp, err := tstemporal.ExecuteSystemOne(ctx, in)
	if err != nil {
		return "", err
	}
	team, err := resp.Choice("team")
	if err != nil {
		return "", err
	}
	return team.Choice, nil
}

func TestWorkflowCompletesThroughTheActivity(t *testing.T) {
	var calls int
	url := apiServer(t, http.StatusOK, okBody(), &calls)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	tstemporal.Register(env, tstemporal.NewActivities(tsClient(t, url)))
	env.RegisterWorkflow(triageWorkflow)

	env.ExecuteWorkflow(triageWorkflow, "My payouts are failing.")

	if !env.IsWorkflowCompleted() {
		t.Fatal("the workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var team string
	if err := env.GetWorkflowResult(&team); err != nil {
		t.Fatalf("result: %v", err)
	}
	if team != "billing" {
		t.Errorf("team = %q, want billing", team)
	}
	if calls != 1 {
		t.Errorf("the API saw %d calls, want 1", calls)
	}
}

// The determinism property, observed: the workflow's commands depend only on
// the activity result, so identical activity results produce identical
// outcomes. A workflow that called the API directly would fail this, because
// Jev is not contractually deterministic and each run would see a different
// answer.
func TestWorkflowIsDeterministicGivenTheSameActivityResult(t *testing.T) {
	const runs = 8
	results := make([]string, 0, runs)

	for i := 0; i < runs; i++ {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.RegisterWorkflow(triageWorkflow)
		// Registered with a nil client: the mock below intercepts every call,
		// so the activity body never runs and never needs one. Registration
		// is still required, because OnActivity resolves the name against the
		// registry.
		tstemporal.Register(env, tstemporal.NewActivities(nil))

		// Mock the activity by name, which is how a replay would supply the
		// recorded result: the workflow never reaches the network.
		env.OnActivity(tstemporal.SystemOneActivityName, mock.Anything, mock.Anything).
			Return(&typesafe.SystemOneResponse{
				Model: "jev-1.13.0",
				Answers: map[string]json.RawMessage{
					"team": json.RawMessage(
						`{"type":"choice","choice":"technical","confidence":0.77,` +
							`"probabilities":{"billing":0.23,"technical":0.77}}`),
				},
				Usage: typesafe.Usage{InputTokens: 100},
			}, nil)

		env.ExecuteWorkflow(triageWorkflow, "the same ticket every time")

		if !env.IsWorkflowCompleted() {
			t.Fatalf("run %d did not complete", i)
		}
		if err := env.GetWorkflowError(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		var team string
		if err := env.GetWorkflowResult(&team); err != nil {
			t.Fatalf("run %d result: %v", i, err)
		}
		results = append(results, team)
	}

	for i, got := range results {
		if got != results[0] {
			t.Fatalf("run %d produced %q, run 0 produced %q", i, got, results[0])
		}
	}
	if results[0] != "technical" {
		t.Errorf("result = %q, want the mocked technical", results[0])
	}
}

// A non-retryable activity failure must reach the workflow rather than being
// retried until the policy runs out.
func TestWorkflowSurfacesANonRetryableFailure(t *testing.T) {
	url := apiServer(t, http.StatusUnauthorized, map[string]any{"detail": "bad key"}, nil)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	tstemporal.Register(env, tstemporal.NewActivities(tsClient(t, url)))
	env.RegisterWorkflow(triageWorkflow)

	env.ExecuteWorkflow(triageWorkflow, "x")

	if !env.IsWorkflowCompleted() {
		t.Fatal("the workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected the workflow to fail")
	}
}

// --- options -----------------------------------------------------------------

func TestDefaultActivityOptionsNameTheNonRetryableTypes(t *testing.T) {
	opts := tstemporal.DefaultActivityOptions
	if opts.StartToCloseTimeout <= 0 {
		t.Error("an activity with no start-to-close timeout is rejected by Temporal")
	}
	if opts.RetryPolicy == nil {
		t.Fatal("no retry policy")
	}
	want := map[string]bool{
		"Authentication": true, "PermissionDenied": true,
		"InvalidRequest": true, "NotFound": true, "Configuration": true,
	}
	for _, got := range opts.RetryPolicy.NonRetryableErrorTypes {
		delete(want, got)
	}
	if len(want) != 0 {
		t.Errorf("these error types are produced by the activity but are retryable: %v", want)
	}
	if opts.RetryPolicy.MaximumInterval > time.Minute {
		t.Error("a maximum backoff over a minute makes a transient failure look like a hang")
	}
}
