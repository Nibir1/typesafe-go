package typesafeotel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/typesafeotel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// --- harness -----------------------------------------------------------------

func recorder(t *testing.T) (*tracetest.SpanRecorder, *trace.TracerProvider) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(sr))
	t.Cleanup(func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return sr, tp
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

func request() *typesafe.SystemOneRequest {
	return &typesafe.SystemOneRequest{
		State: "Help! My payouts have been failing.",
		Model: "jev-latest",
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
			"team": typesafe.Choice{
				Instructions: "Which team?",
				Criteria:     typesafe.Options{"billing": "money", "technical": "bugs"},
			},
		},
	}
}

// attrs flattens a span's attributes for lookup.
func attrs(s trace.ReadOnlySpan) map[string]attribute.Value {
	out := map[string]attribute.Value{}
	for _, kv := range s.Attributes() {
		out[string(kv.Key)] = kv.Value
	}
	return out
}

func spansByName(sr *tracetest.SpanRecorder) map[string][]trace.ReadOnlySpan {
	out := map[string][]trace.ReadOnlySpan{}
	for _, s := range sr.Ended() {
		out[s.Name()] = append(out[s.Name()], s)
	}
	return out
}

// --- the span tree -----------------------------------------------------------

func TestSuccessfulCallProducesOneCallAndOneAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	sr, tp := recorder(t)
	tracer := typesafeotel.New(typesafeotel.WithTracerProvider(tp))

	client, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		typesafe.WithInterceptor(tracer.Interceptor()),
		typesafe.WithRetryObserver(tracer.RetryObserver()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	byName := spansByName(sr)
	if n := len(byName[typesafeotel.SpanCall]); n != 1 {
		t.Fatalf("%d %s spans, want 1", n, typesafeotel.SpanCall)
	}
	if n := len(byName["typesafe.attempt 1"]); n != 1 {
		t.Fatalf("%d attempt spans, want 1 — the successful attempt must be recorded too", n)
	}

	call := byName[typesafeotel.SpanCall][0]
	a := attrs(call)
	for key, want := range map[string]any{
		typesafeotel.AttrSystem:        "typesafe",
		typesafeotel.AttrRequestModel:  "jev-latest",
		typesafeotel.AttrResponseModel: "jev-1.13.0",
	} {
		if got := a[key].AsString(); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := a[typesafeotel.AttrInputTokens].AsInt64(); got != 312 {
		t.Errorf("%s = %d, want 312", typesafeotel.AttrInputTokens, got)
	}
	if got := a[typesafeotel.AttrQuestionCount].AsInt64(); got != 2 {
		t.Errorf("%s = %d, want 2", typesafeotel.AttrQuestionCount, got)
	}
	if got := a[typesafeotel.AttrRetryCount].AsInt64(); got != 0 {
		t.Errorf("%s = %d, want 0", typesafeotel.AttrRetryCount, got)
	}
	types := a[typesafeotel.AttrQuestionTypes].AsStringSlice()
	sort.Strings(types)
	if len(types) != 2 || types[0] != "choice" || types[1] != "noul" {
		t.Errorf("%s = %v, want [choice noul]", typesafeotel.AttrQuestionTypes, types)
	}
	if call.Status().Code != codes.Ok {
		t.Errorf("status = %v, want Ok", call.Status().Code)
	}
	if call.SpanKind() != oteltrace.SpanKindClient {
		t.Errorf("kind = %v, want client", call.SpanKind())
	}

	// Per-answer attributes: a Noul has no confidence, so its probability is
	// what gets recorded.
	if got := a[typesafeotel.AttrNoulPfx+"is_urgent"].AsFloat64(); got != 0.92 {
		t.Errorf("noul attribute = %v, want 0.92", got)
	}
	if got := a[typesafeotel.AttrConfidencePfx+"team"].AsFloat64(); got != 0.81 {
		t.Errorf("confidence attribute = %v, want 0.81", got)
	}
	if _, present := a[typesafeotel.AttrConfidencePfx+"is_urgent"]; present {
		t.Error("a Noul has no confidence; recording one would invent a number")
	}
}

// The headline exit criterion: a retried call must produce a tree, with every
// attempt parented to the call.
func TestRetriedCallProducesAParentedAttemptTree(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"detail": "slow down"})
			return
		}
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	sr, tp := recorder(t)
	tracer := typesafeotel.New(typesafeotel.WithTracerProvider(tp))

	client, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.RetryPolicy{
			MaxRetries:     3,
			BackoffInitial: time.Millisecond,
			BackoffMax:     2 * time.Millisecond,
			HTTPStatuses:   map[int]bool{429: true},
			Timeout:        10 * time.Second,
		}),
		typesafe.WithInterceptor(tracer.Interceptor()),
		typesafe.WithRetryObserver(tracer.RetryObserver()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	byName := spansByName(sr)
	call := byName[typesafeotel.SpanCall]
	if len(call) != 1 {
		t.Fatalf("%d call spans, want 1", len(call))
	}
	parent := call[0]

	// Three attempts: two 429s and the success.
	var attempts []trace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() != typesafeotel.SpanCall {
			attempts = append(attempts, s)
		}
	}
	if len(attempts) != 3 {
		names := make([]string, len(attempts))
		for i, s := range attempts {
			names[i] = s.Name()
		}
		t.Fatalf("%d attempt spans (%v), want 3", len(attempts), names)
	}

	// Every attempt is a child of the call, in the same trace.
	for _, s := range attempts {
		if s.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Errorf("%s is not parented to the call span", s.Name())
		}
		if s.SpanContext().TraceID() != parent.SpanContext().TraceID() {
			t.Errorf("%s is in a different trace", s.Name())
		}
	}

	// Named and numbered in order.
	sort.Slice(attempts, func(i, j int) bool {
		return attrs(attempts[i])[typesafeotel.AttrAttemptNumber].AsInt64() <
			attrs(attempts[j])[typesafeotel.AttrAttemptNumber].AsInt64()
	})
	for i, s := range attempts {
		want := int64(i + 1)
		if got := attrs(s)[typesafeotel.AttrAttemptNumber].AsInt64(); got != want {
			t.Errorf("attempt span %d is numbered %d", i, got)
		}
	}

	// The two failures carry their status and are marked as errors; the last
	// one succeeded.
	for i, s := range attempts[:2] {
		a := attrs(s)
		if got := a[typesafeotel.AttrAttemptStatus].AsInt64(); got != 429 {
			t.Errorf("attempt %d status = %d, want 429", i+1, got)
		}
		if s.Status().Code != codes.Error {
			t.Errorf("attempt %d status code = %v, want Error", i+1, s.Status().Code)
		}
		if len(s.Events()) == 0 {
			t.Errorf("attempt %d recorded no error event", i+1)
		}
	}
	if attempts[2].Status().Code != codes.Ok {
		t.Errorf("the final attempt should be Ok, got %v", attempts[2].Status().Code)
	}

	// And the call span counts the retries.
	if got := attrs(parent)[typesafeotel.AttrRetryCount].AsInt64(); got != 2 {
		t.Errorf("%s = %d, want 2", typesafeotel.AttrRetryCount, got)
	}
	if parent.Status().Code != codes.Ok {
		t.Errorf("the call succeeded; status = %v", parent.Status().Code)
	}
}

// Attempt spans must not all collapse to the same instant: the timings are the
// reason to draw the tree at all.
func TestAttemptSpansHaveDistinctTimings(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		time.Sleep(5 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"detail": "boom"})
			return
		}
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	sr, tp := recorder(t)
	tracer := typesafeotel.New(typesafeotel.WithTracerProvider(tp))

	client, _ := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.RetryPolicy{
			MaxRetries: 2, BackoffInitial: time.Millisecond, BackoffMax: time.Millisecond,
			HTTPStatuses: map[int]bool{500: true}, Timeout: 10 * time.Second,
		}),
		typesafe.WithInterceptor(tracer.Interceptor()),
		typesafe.WithRetryObserver(tracer.RetryObserver()),
	)

	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	for _, s := range sr.Ended() {
		if s.Name() == typesafeotel.SpanCall {
			continue
		}
		if d := s.EndTime().Sub(s.StartTime()); d <= 0 {
			t.Errorf("%s has a non-positive duration %s", s.Name(), d)
		}
	}

	// And every attempt fits inside the call.
	var parent trace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() == typesafeotel.SpanCall {
			parent = s
		}
	}
	if parent == nil {
		t.Fatal("no call span")
	}
	for _, s := range sr.Ended() {
		if s.Name() == typesafeotel.SpanCall {
			continue
		}
		if s.StartTime().Before(parent.StartTime()) {
			t.Errorf("%s starts before the call it belongs to", s.Name())
		}
	}
}

// --- failure -----------------------------------------------------------------

func TestFailedCallIsMarkedAsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": "bad key"})
	}))
	defer srv.Close()

	sr, tp := recorder(t)
	tracer := typesafeotel.New(typesafeotel.WithTracerProvider(tp))

	client, _ := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		typesafe.WithInterceptor(tracer.Interceptor()),
		typesafe.WithRetryObserver(tracer.RetryObserver()),
	)

	if _, err := client.SystemOne(context.Background(), request()); err == nil {
		t.Fatal("expected an error")
	}

	var call trace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() == typesafeotel.SpanCall {
			call = s
		}
	}
	if call == nil {
		t.Fatal("no call span")
	}
	if call.Status().Code != codes.Error {
		t.Errorf("status = %v, want Error", call.Status().Code)
	}
	if len(call.Events()) == 0 {
		t.Error("the error was not recorded on the span")
	}
	// No model or token attributes on a failure: reporting zeros would put a
	// zero in every histogram built on them.
	a := attrs(call)
	if _, present := a[typesafeotel.AttrResponseModel]; present {
		t.Error("a failed call has no response model")
	}
	if _, present := a[typesafeotel.AttrInputTokens]; present {
		t.Error("a failed call has no usage")
	}
}

// --- span linkage in a wider trace -------------------------------------------

// The call span must attach to whatever span the caller already had open,
// rather than starting a new trace.
func TestCallSpanIsAChildOfTheCallersSpan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	sr, tp := recorder(t)
	tracer := typesafeotel.New(typesafeotel.WithTracerProvider(tp))

	client, _ := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		typesafe.WithInterceptor(tracer.Interceptor()),
	)

	ctx, outer := tp.Tracer("test").Start(context.Background(), "handle-ticket")
	if _, err := client.SystemOne(ctx, request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	outer.End()

	var call, root trace.ReadOnlySpan
	for _, s := range sr.Ended() {
		switch s.Name() {
		case typesafeotel.SpanCall:
			call = s
		case "handle-ticket":
			root = s
		}
	}
	if call == nil || root == nil {
		t.Fatal("expected both spans")
	}
	if call.Parent().SpanID() != root.SpanContext().SpanID() {
		t.Error("the call span did not attach to the caller's span")
	}
	if call.SpanContext().TraceID() != root.SpanContext().TraceID() {
		t.Error("the call span started a new trace")
	}
}

// --- options -----------------------------------------------------------------

func TestAnswerAttributesCanBeTurnedOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	sr, tp := recorder(t)
	tracer := typesafeotel.New(
		typesafeotel.WithTracerProvider(tp),
		typesafeotel.WithAnswerAttributes(false),
	)

	client, _ := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		typesafe.WithInterceptor(tracer.Interceptor()),
	)
	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	for _, s := range sr.Ended() {
		for k := range attrs(s) {
			if len(k) > len(typesafeotel.AttrConfidencePfx) &&
				k[:len(typesafeotel.AttrConfidencePfx)] == typesafeotel.AttrConfidencePfx {
				t.Errorf("answer attribute %q was recorded despite being turned off", k)
			}
		}
	}
	// Usage attributes are not answer attributes, and must still be there.
	for _, s := range sr.Ended() {
		if s.Name() == typesafeotel.SpanCall {
			if _, ok := attrs(s)[typesafeotel.AttrInputTokens]; !ok {
				t.Error("usage attributes were dropped along with the answer ones")
			}
		}
	}
}

// A retry on a call this tracer did not open must not produce an orphan span.
func TestRetryObserverWithoutACallSpanIsIgnored(t *testing.T) {
	sr, tp := recorder(t)
	tracer := typesafeotel.New(typesafeotel.WithTracerProvider(tp))

	tracer.RetryObserver()(context.Background(), typesafe.AttemptInfo{Attempt: 1, Status: 500})

	if n := len(sr.Ended()); n != 0 {
		t.Errorf("%d spans emitted for an untraced call, want 0", n)
	}
}

func TestNewUsesTheGlobalProviderByDefault(t *testing.T) {
	tracer := typesafeotel.New()
	if tracer == nil {
		t.Fatal("New returned nil")
	}
	// The global provider is a no-op by default, so this just has to not
	// panic and not emit.
	tracer.RetryObserver()(context.Background(), typesafe.AttemptInfo{Attempt: 1})
}
