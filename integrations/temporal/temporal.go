// Package temporal runs TypeSafe calls as Temporal Activities.
//
//	acts := tstemporal.NewActivities(client)
//	w := worker.New(c, "triage", worker.Options{})
//	tstemporal.Register(w, acts)
//	w.RegisterWorkflow(TriageWorkflow)
//
//	func TriageWorkflow(ctx workflow.Context, ticket string) (string, error) {
//	    in, err := tstemporal.NewInput(ticket, questions)
//	    if err != nil {
//	        return "", err
//	    }
//	    out, err := tstemporal.ExecuteSystemOne(ctx, in)
//	    ...
//	}
//
// # The rule this package exists to enforce
//
// **The API call goes in an Activity. Never in workflow code.**
//
// Workflow code is replayed. Temporal re-executes it from the event history
// after a worker restart, a deploy, or a continue-as-new, and it must produce
// the same commands every time or the workflow task fails with a
// non-determinism error. An HTTP call is not replayable: the second run would
// make a second call, get a different answer — Jev is documented as highly
// consistent but is not contractually deterministic — and diverge.
//
// An Activity's *result* is recorded in the history. On replay the recorded
// result is handed back without re-running anything, which is exactly what
// makes a model call safe here. That is not a style preference; it is the only
// arrangement that works.
//
// # Retries belong to one layer, not two
//
// The SDK retries internally, and Temporal retries Activities. Left alone they
// multiply: three SDK attempts inside three Temporal attempts is nine calls
// for one logical request, and the Activity's start-to-close timeout has to
// cover the SDK's own backoff before Temporal sees a failure at all.
//
// Pick one. The recommendation is Temporal, because its retries survive a
// worker crash and are visible in the UI:
//
//	client, _ := typesafe.NewClient(typesafe.WithRetryPolicy(typesafe.NoRetry()))
//
// DefaultActivityOptions is built for that arrangement.
//
// # Budgets do not span workers
//
// A typesafe.Budget counts in one process. With several workers, each enforces
// the cap independently and the account sees the sum. Size it per worker, or
// enforce the real limit with a Temporal task-queue rate limit, which is
// cluster-wide.
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	typesafe "github.com/nibir1/typesafe-go"
)

// Activity names, as registered. Exported so a workflow can refer to one by
// name rather than by function value, which is what a polyglot worker fleet
// needs.
const (
	SystemOneActivityName = "typesafe.SystemOne"
	ModelsActivityName    = "typesafe.Models"
)

// SystemOneInput is one evaluation, in a form Temporal can serialize.
//
// Questions are RawQuestion — a plain map — rather than typesafe.Question.
// Question is a sealed interface, and Temporal has to *deserialize* the input
// on the worker side: an interface with no concrete type to decode into cannot
// round-trip. NewInput converts typed questions into this form, so the typed
// constructors and the analyzers still apply where the questions are written.
type SystemOneInput struct {
	// State is the content every question refers to.
	State any `json:"state"`

	// Model selects the model. Empty uses the client's default.
	Model string `json:"model,omitempty"`

	// Questions must contain at least one entry.
	Questions map[string]typesafe.RawQuestion `json:"questions"`
}

// NewInput converts typed questions into a serializable activity input.
//
//	in, err := tstemporal.NewInput(ticket, typesafe.Questions{
//	    "is_urgent": typesafe.Noul{Instructions: "Urgent?"},
//	})
//
// Call it in workflow code: it only marshals, makes no network call, and is
// deterministic, so it is safe there. Validation happens in the Activity,
// where a failure can be retried.
func NewInput(state any, questions typesafe.Questions) (SystemOneInput, error) {
	in := SystemOneInput{State: state, Questions: make(map[string]typesafe.RawQuestion, len(questions))}
	for id, q := range questions {
		raw, err := toRaw(q)
		if err != nil {
			return SystemOneInput{}, fmt.Errorf("typesafe/temporal: question %q: %w", id, err)
		}
		in.Questions[id] = raw
	}
	return in, nil
}

// WithModel returns a copy of in pinned to a model.
func (in SystemOneInput) WithModel(model string) SystemOneInput {
	in.Model = model
	return in
}

func toRaw(q typesafe.Question) (typesafe.RawQuestion, error) {
	if raw, ok := q.(typesafe.RawQuestion); ok {
		return raw, nil
	}
	b, err := json.Marshal(q)
	if err != nil {
		return nil, fmt.Errorf("marshaling: %w", err)
	}
	var out typesafe.RawQuestion
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decoding: %w", err)
	}
	return out, nil
}

// Activities holds the client and exposes the registerable activities.
//
// One Activities value per worker: the client is safe for concurrent use and
// its connection pool, circuit breaker and budget only mean anything when
// shared.
type Activities struct {
	client *typesafe.Client
}

// NewActivities wraps a client.
func NewActivities(client *typesafe.Client) *Activities {
	return &Activities{client: client}
}

// ActivityRegistry is the part of a worker that Register needs.
//
// Narrower than worker.Registry on purpose: Temporal's test environments
// register activities but do not implement the whole interface, and a
// signature that demands methods it never calls would make the activities
// untestable in the environment built for testing them.
type ActivityRegistry interface {
	RegisterActivityWithOptions(a any, options activity.RegisterOptions)
}

// Register adds the activities under their exported names.
//
// Takes a worker.Worker, a TestActivityEnvironment or a TestWorkflowEnvironment
// — anything that can register an activity.
func Register(r ActivityRegistry, a *Activities) {
	r.RegisterActivityWithOptions(a.SystemOne, activity.RegisterOptions{Name: SystemOneActivityName})
	r.RegisterActivityWithOptions(a.Models, activity.RegisterOptions{Name: ModelsActivityName})
}

// SystemOne is the Activity that makes the call.
//
// It returns typed non-retryable failures for the errors that will never
// succeed on a retry — a bad key, a malformed request — so Temporal stops
// immediately instead of burning the whole retry policy on a request that
// cannot work. Everything else is left retryable.
func (a *Activities) SystemOne(ctx context.Context, in SystemOneInput) (*typesafe.SystemOneResponse, error) {
	if a.client == nil {
		return nil, temporal.NewNonRetryableApplicationError(
			"no TypeSafe client on the worker", "Configuration", nil)
	}

	questions := make(typesafe.Questions, len(in.Questions))
	for id, raw := range in.Questions {
		questions[id] = raw
	}

	req := &typesafe.SystemOneRequest{State: in.State, Model: in.Model, Questions: questions}
	resp, err := a.client.SystemOne(ctx, req)
	if err != nil {
		return nil, classify(err)
	}
	return resp, nil
}

// Models is the Activity that lists available models.
func (a *Activities) Models(ctx context.Context) ([]typesafe.ModelCard, error) {
	if a.client == nil {
		return nil, temporal.NewNonRetryableApplicationError(
			"no TypeSafe client on the worker", "Configuration", nil)
	}
	cards, err := a.client.Models(ctx)
	if err != nil {
		return nil, classify(err)
	}
	return cards, nil
}

// classify marks the failures a retry cannot fix.
//
// Retrying a 401 until the policy is exhausted turns a five-second failure
// into a five-minute one and tells the operator nothing new. Retrying a 429,
// by contrast, is exactly right.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var (
		auth    *typesafe.AuthenticationError
		perm    *typesafe.PermissionDeniedError
		badReq  *typesafe.BadRequestError
		unproc  *typesafe.UnprocessableEntityError
		missing *typesafe.NotFoundError
	)
	switch {
	case errors.As(err, &auth):
		return temporal.NewNonRetryableApplicationError(err.Error(), "Authentication", err)
	case errors.As(err, &perm):
		return temporal.NewNonRetryableApplicationError(err.Error(), "PermissionDenied", err)
	case errors.As(err, &badReq), errors.As(err, &unproc):
		return temporal.NewNonRetryableApplicationError(err.Error(), "InvalidRequest", err)
	case errors.As(err, &missing):
		return temporal.NewNonRetryableApplicationError(err.Error(), "NotFound", err)
	}
	// Validation that never reached the network is equally hopeless on a
	// retry: the same input produces the same refusal.
	if errors.Is(err, typesafe.ErrInvalidRequest) {
		return temporal.NewNonRetryableApplicationError(err.Error(), "InvalidRequest", err)
	}
	return err
}

// DefaultActivityOptions are sensible options for a TypeSafe activity.
//
// Built for the arrangement where Temporal owns the retries and the SDK does
// not. StartToCloseTimeout is 60s — generous against the documented 70–500ms
// so a slow call is not cut off, tight enough that a hung connection is not
// mistaken for work in progress.
//
// The non-retryable list is belt and braces: the Activity already converts
// those to non-retryable errors, and naming them here means the policy still
// holds for anyone who calls the client directly from their own activity.
var DefaultActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 60 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
		MaximumInterval:    30 * time.Second,
		MaximumAttempts:    5,
		NonRetryableErrorTypes: []string{
			"Authentication", "PermissionDenied", "InvalidRequest", "NotFound", "Configuration",
		},
	},
}

// ExecuteSystemOne runs the Activity from workflow code and waits for it.
//
//	out, err := tstemporal.ExecuteSystemOne(ctx, in)
//
// Options come from the workflow context when it already has them, so a
// workflow that set its own ActivityOptions keeps them; DefaultActivityOptions
// is used only when it did not.
func ExecuteSystemOne(ctx workflow.Context, in SystemOneInput, opts ...workflow.ActivityOptions) (*typesafe.SystemOneResponse, error) {
	ctx = withOptions(ctx, opts...)
	var out typesafe.SystemOneResponse
	if err := workflow.ExecuteActivity(ctx, SystemOneActivityName, in).Get(ctx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExecuteModels runs the models Activity from workflow code.
func ExecuteModels(ctx workflow.Context, opts ...workflow.ActivityOptions) ([]typesafe.ModelCard, error) {
	ctx = withOptions(ctx, opts...)
	var out []typesafe.ModelCard
	if err := workflow.ExecuteActivity(ctx, ModelsActivityName).Get(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func withOptions(ctx workflow.Context, opts ...workflow.ActivityOptions) workflow.Context {
	if len(opts) > 0 {
		return workflow.WithActivityOptions(ctx, opts[0])
	}
	if info := workflow.GetActivityOptions(ctx); info.StartToCloseTimeout > 0 || info.ScheduleToCloseTimeout > 0 {
		return ctx
	}
	return workflow.WithActivityOptions(ctx, DefaultActivityOptions)
}
