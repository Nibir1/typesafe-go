// Package typesafeotel traces TypeSafe calls with OpenTelemetry.
//
//	tracer := typesafeotel.New()
//	client, err := typesafe.NewClient(
//	    typesafe.WithInterceptor(tracer.Interceptor()),
//	    typesafe.WithRetryObserver(tracer.RetryObserver()),
//	)
//
// Install both. The interceptor opens the span for the logical call; the retry
// observer is what turns a retried call from one long span into a tree that
// shows where the time actually went.
//
//	typesafe.systemone                    412ms
//	├── typesafe.attempt 1                 38ms  error, 429
//	├── typesafe.attempt 2                 41ms  error, 429
//	└── typesafe.attempt 3                310ms  ok
//
// # Semantic conventions, and where System One does not fit them
//
// Attributes follow the OpenTelemetry GenAI conventions where they apply:
// gen_ai.system, gen_ai.request.model, gen_ai.response.model,
// gen_ai.usage.input_tokens and gen_ai.usage.output_tokens.
//
// Three places where the conventions do not describe this API, recorded here
// rather than forced into a shape that would mislead a dashboard built on
// them:
//
//   - Output tokens are reported but not billed, and they do not mean
//     "generated text" — System One returns probabilities, not completions. A
//     cost dashboard summing gen_ai.usage.output_tokens is measuring nothing.
//   - There is no gen_ai.operation.name that fits. The conventions enumerate
//     chat, text_completion and embeddings; this is none of them, so the
//     operation is reported as "systemone" under the same key, which is the
//     least surprising thing to show in a trace view.
//   - Confidence has no equivalent at all. It is the single most useful number
//     in a System One response, so it is recorded under typesafe.* keys rather
//     than omitted for the sake of conformance.
package typesafeotel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	typesafe "github.com/nibir1/typesafe-go"
)

// ScopeName is the instrumentation scope these spans are emitted under.
const ScopeName = "github.com/nibir1/typesafe-go/typesafeotel"

// Span names.
const (
	// SpanCall is the logical call, retries included.
	SpanCall = "typesafe.systemone"

	// SpanAttempt is one HTTP attempt within a call. The attempt number is
	// appended: "typesafe.attempt 1".
	SpanAttempt = "typesafe.attempt"
)

// Attribute keys.
//
// The gen_ai.* keys come from the OpenTelemetry GenAI semantic conventions.
// The typesafe.* keys are this SDK's own, for things the conventions do not
// describe.
const (
	AttrSystem        = "gen_ai.system"
	AttrOperation     = "gen_ai.operation.name"
	AttrRequestModel  = "gen_ai.request.model"
	AttrResponseModel = "gen_ai.response.model"
	AttrInputTokens   = "gen_ai.usage.input_tokens"
	AttrOutputTokens  = "gen_ai.usage.output_tokens"

	AttrQuestionCount  = "typesafe.question.count"
	AttrQuestionTypes  = "typesafe.question.types"
	AttrRequestID      = "typesafe.request_id"
	AttrRetryCount     = "typesafe.retry.count"
	AttrAttemptNumber  = "typesafe.attempt.number"
	AttrAttemptStatus  = "typesafe.attempt.http_status"
	AttrAttemptDelay   = "typesafe.attempt.backoff_ms"
	AttrRetryAfterUsed = "typesafe.attempt.retry_after_honored"
	AttrConfidencePfx  = "typesafe.answer.confidence."
	AttrNoulPfx        = "typesafe.answer.noul."
)

// SystemName is the value of gen_ai.system.
const SystemName = "typesafe"

// Option configures a Tracer.
type Option func(*Tracer)

// WithTracerProvider sets the provider. The global one is used by default.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(t *Tracer) {
		if tp != nil {
			t.provider = tp
		}
	}
}

// WithAnswerAttributes controls whether per-answer confidence and probability
// attributes are recorded. On by default.
//
// Turn it off for questions whose ids are sensitive, or when a request asks
// enough questions that one attribute per answer would bloat the span. The
// values themselves are never sensitive — they are numbers — but a question id
// can be.
func WithAnswerAttributes(on bool) Option {
	return func(t *Tracer) { t.answerAttrs = on }
}

// Tracer produces the interceptor and retry observer.
type Tracer struct {
	provider    trace.TracerProvider
	tracer      trace.Tracer
	answerAttrs bool
}

// New builds a Tracer.
func New(opts ...Option) *Tracer {
	t := &Tracer{answerAttrs: true}
	for _, o := range opts {
		o(t)
	}
	if t.provider == nil {
		t.provider = otel.GetTracerProvider()
	}
	t.tracer = t.provider.Tracer(ScopeName)
	return t
}

// callState is the per-call bookkeeping the retry observer needs.
type callState struct {
	mu sync.Mutex

	// span is the logical call's span, the parent of every attempt.
	span trace.Span

	// ctx carries span so attempt spans are created as its children.
	ctx context.Context

	// started is when the call began, and attemptFrom is when the current
	// attempt began. AttemptInfo reports Elapsed rather than timestamps, so
	// attempt boundaries are reconstructed from it.
	started     time.Time
	attemptFrom time.Time

	// retries counts observed retries, which is one less than the number of
	// attempts made.
	retries int
}

type callKey struct{}

// Interceptor returns the interceptor that opens the logical span.
//
// Place it outermost, so its span is the parent of everything else you
// instrument.
func (t *Tracer) Interceptor() typesafe.Interceptor {
	return func(next typesafe.Handler) typesafe.Handler {
		return func(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
			attrs := []attribute.KeyValue{
				attribute.String(AttrSystem, SystemName),
				attribute.String(AttrOperation, "systemone"),
			}
			if req != nil {
				attrs = append(attrs,
					attribute.Int(AttrQuestionCount, len(req.Questions)),
					attribute.StringSlice(AttrQuestionTypes, questionTypes(req.Questions)),
				)
				if req.Model != "" {
					attrs = append(attrs, attribute.String(AttrRequestModel, req.Model))
				}
			}

			ctx, span := t.tracer.Start(ctx, SpanCall,
				trace.WithSpanKind(trace.SpanKindClient),
				trace.WithAttributes(attrs...),
			)
			defer span.End()

			now := time.Now()
			state := &callState{span: span, ctx: ctx, started: now, attemptFrom: now}
			ctx = context.WithValue(ctx, callKey{}, state)

			resp, err := next(ctx, req)

			// The final attempt is not reported by the retry observer — it
			// only fires on a failure that will be retried — so it is closed
			// out here. Without this the trace would show every attempt but
			// the one that actually produced the answer.
			state.finishAttempt(t, err, 0, 0, false)

			if id := typesafe.RequestIDFrom(ctx); id != "" {
				span.SetAttributes(attribute.String(AttrRequestID, id))
			}
			state.mu.Lock()
			retries := state.retries
			state.mu.Unlock()
			span.SetAttributes(attribute.Int(AttrRetryCount, retries))

			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				return resp, err
			}
			if resp != nil {
				span.SetAttributes(
					attribute.String(AttrResponseModel, resp.Model),
					attribute.Int(AttrInputTokens, resp.Usage.InputTokens),
					attribute.Int(AttrOutputTokens, resp.Usage.OutputTokens),
				)
				if t.answerAttrs {
					span.SetAttributes(answerAttributes(resp)...)
				}
			}
			span.SetStatus(codes.Ok, "")
			return resp, err
		}
	}
}

// RetryObserver returns the observer that records one span per failed attempt.
//
//	typesafe.WithRetryObserver(tracer.RetryObserver())
//
// Without it the trace still shows the call, but a 900ms span with no
// explanation. With it, the three attempts and the backoff between them are
// visible, which is the difference between "the API was slow" and "we were
// rate limited twice".
func (t *Tracer) RetryObserver() func(context.Context, typesafe.AttemptInfo) {
	return func(ctx context.Context, info typesafe.AttemptInfo) {
		state, ok := ctx.Value(callKey{}).(*callState)
		if !ok {
			// A retry on a call this tracer did not open. Emitting an orphan
			// span would put a fragment of a trace in the dashboard with
			// nothing to attach it to, which is worse than omitting it.
			return
		}
		state.finishAttempt(t, info.Err, info.Status, info.Delay, info.RetryAfterHonored)

		state.mu.Lock()
		state.retries++
		// The next attempt starts after this backoff, not now.
		state.attemptFrom = time.Now().Add(info.Delay)
		state.mu.Unlock()
	}
}

// finishAttempt records a child span covering the attempt that just ended.
//
// The span is created retrospectively, with explicit start and end times,
// because an attempt is only observable once it has finished: AttemptInfo
// arrives after the fact, and the successful attempt is not reported at all.
// Backdating is what makes the tree's timings line up with reality instead of
// collapsing every attempt to a point.
func (s *callState) finishAttempt(t *Tracer, err error, status int, delay time.Duration, retryAfter bool) {
	s.mu.Lock()
	start := s.attemptFrom
	n := s.retries + 1
	parentCtx := s.ctx
	s.mu.Unlock()

	end := time.Now()
	if start.After(end) {
		// A backoff that has not elapsed yet; nothing sensible to draw.
		start = end
	}

	attrs := []attribute.KeyValue{
		attribute.Int(AttrAttemptNumber, n),
	}
	if status != 0 {
		attrs = append(attrs, attribute.Int(AttrAttemptStatus, status))
	}
	if delay > 0 {
		attrs = append(attrs,
			attribute.Int64(AttrAttemptDelay, delay.Milliseconds()),
			attribute.Bool(AttrRetryAfterUsed, retryAfter),
		)
	}

	_, span := t.tracer.Start(parentCtx, fmt.Sprintf("%s %d", SpanAttempt, n),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(start),
		trace.WithAttributes(attrs...),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End(trace.WithTimestamp(end))
}

// questionTypes returns the distinct question types in a request, sorted.
//
// Sorted because an attribute built from map iteration would differ between
// otherwise identical spans, which makes it useless for grouping.
func questionTypes(qs map[string]typesafe.Question) []string {
	seen := map[string]bool{}
	for _, q := range qs {
		seen[questionType(q)] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// typeFromJSON reads the wire discriminator out of a marshaled question.
func typeFromJSON(b []byte) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &probe); err != nil || probe.Type == "" {
		return "unknown"
	}
	return probe.Type
}

func questionType(q typesafe.Question) string {
	switch q.(type) {
	case typesafe.Noul, typesafe.NoulBuilder:
		return typesafe.TypeNoul
	case typesafe.Choice, typesafe.ChoiceBuilder:
		return typesafe.TypeChoice
	case typesafe.Score, typesafe.ScoreBuilder:
		return typesafe.TypeScore
	case typesafe.RawQuestion:
		return "raw"
	default:
		// The typed wrappers embed the plain question, so they match none of
		// the cases above. Ask the wire form what it is rather than
		// enumerating every wrapper here and going stale the next time one is
		// added.
		if m, ok := q.(interface{ MarshalJSON() ([]byte, error) }); ok {
			if b, err := m.MarshalJSON(); err == nil {
				return typeFromJSON(b)
			}
		}
		return "unknown"
	}
}

// answerAttributes records confidence per answer, and the probability of each
// Noul.
//
// Confidence is the number a dashboard should be built on: a drop in mean
// confidence is the earliest visible sign that the inputs have changed shape,
// and it shows up long before anyone notices the answers are wrong. A Noul has
// no confidence — its probability is the uncertainty — so that is recorded
// instead.
func answerAttributes(resp *typesafe.SystemOneResponse) []attribute.KeyValue {
	if resp == nil {
		return nil
	}
	ids := make([]string, 0, len(resp.Answers))
	for id := range resp.Answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	attrs := make([]attribute.KeyValue, 0, len(ids))
	for _, id := range ids {
		a, err := resp.Answer(id)
		if err != nil {
			continue
		}
		switch v := a.(type) {
		case typesafe.NoulAnswer:
			attrs = append(attrs, attribute.Float64(AttrNoulPfx+id, v.Noul))
		case typesafe.ChoiceAnswer:
			attrs = append(attrs, attribute.Float64(AttrConfidencePfx+id, v.Confidence))
		case typesafe.ScoreAnswer:
			attrs = append(attrs, attribute.Float64(AttrConfidencePfx+id, v.Confidence))
		}
	}
	return attrs
}
