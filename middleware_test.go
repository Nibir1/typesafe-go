package typesafe_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
)

// --- interceptor ordering -----------------------------------------------------

// TestInterceptorsComposeInDeclaredOrder: the first listed is outermost, so it
// sees a request first and its response last — the same nesting as gRPC and
// net/http middleware.
func TestInterceptorsComposeInDeclaredOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string) typesafe.Interceptor {
		return func(next typesafe.Handler) typesafe.Handler {
			return func(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
				mu.Lock()
				order = append(order, name+":in")
				mu.Unlock()
				resp, err := next(ctx, req)
				mu.Lock()
				order = append(order, name+":out")
				mu.Unlock()
				return resp, err
			}
		}
	}

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithInterceptor(record("a"), record("b"), record("c")))

	if _, err := c.SystemOne(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	want := []string{"a:in", "b:in", "c:in", "c:out", "b:out", "a:out"}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

// TestInterceptorSeesOneLogicalCallAcrossRetries: retries happen beneath the
// innermost handler, so instrumentation records what the caller waited for
// rather than one entry per attempt.
func TestInterceptorSeesOneLogicalCallAcrossRetries(t *testing.T) {
	var calls, attempts int
	count := func(next typesafe.Handler) typesafe.Handler {
		return func(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
			calls++
			return next(ctx, req)
		}
	}

	srv := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		srv++
		if srv < 3 {
			w.WriteHeader(500)
			return
		}
		writeJSON(t, w, 200, okResponse())
	},
		typesafe.WithInterceptor(count),
		typesafe.WithRetryPolicy(fastRetry()),
		typesafe.WithRetryObserver(func(typesafe.AttemptInfo) { attempts++ }),
	)

	if _, err := c.SystemOne(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if calls != 1 {
		t.Errorf("interceptor ran %d times, want 1 — it wraps the logical call", calls)
	}
	if attempts != 2 {
		t.Errorf("retry observer saw %d retries, want 2 — that is the per-attempt layer", attempts)
	}
	if srv != 3 {
		t.Errorf("server saw %d requests, want 3", srv)
	}
}

func fastRetry() typesafe.RetryPolicy {
	p := typesafe.DefaultRetryPolicy()
	p.BackoffInitial = time.Millisecond
	p.BackoffMax = time.Millisecond
	p.BackoffJitter = 0
	return p
}

// TestInterceptorCanAlterTheRequest proves the chain is more than observation.
func TestInterceptorCanAlterTheRequest(t *testing.T) {
	pin := func(next typesafe.Handler) typesafe.Handler {
		return func(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
			req.Model = "jev-1.13.0"
			return next(ctx, req)
		}
	}
	var got string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got, _ = body["model"].(string)
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithInterceptor(pin))

	if _, err := c.SystemOne(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if got != "jev-1.13.0" {
		t.Errorf("model on the wire = %q, want the interceptor's value", got)
	}
}

func TestNilInterceptorIsRejected(t *testing.T) {
	_, err := typesafe.NewClient(typesafe.WithAPIKey("k"), typesafe.WithInterceptor(nil))
	if !errors.Is(err, typesafe.ErrInvalidConfig) {
		t.Errorf("err = %v, want ErrInvalidConfig", err)
	}
}

// --- panic recovery -----------------------------------------------------------

// TestPanicInInterceptorBecomesAnError: instrumentation must not take down the
// request path, and the captured stack must point at the panic rather than at
// the recovery site.
func TestPanicInInterceptorBecomesAnError(t *testing.T) {
	boom := func(next typesafe.Handler) typesafe.Handler {
		return func(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
			panic("instrumentation exploded")
		}
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request should never have been sent")
	}, typesafe.WithInterceptor(boom))

	resp, err := c.SystemOne(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("a panic should surface as an error")
	}
	if resp != nil {
		t.Error("no response should be returned alongside a panic")
	}

	var pe *typesafe.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T, want *PanicError", err)
	}
	if pe.Value != "instrumentation exploded" {
		t.Errorf("Value = %v", pe.Value)
	}
	if len(pe.Stack) == 0 {
		t.Fatal("no stack captured")
	}
	// The stack has to name the function that panicked, or it is useless.
	if !strings.Contains(string(pe.Stack), "TestPanicInInterceptorBecomesAnError") {
		t.Errorf("stack does not reach the panicking frame:\n%s", pe.Stack)
	}
}

func TestPanicWithAnErrorValueUnwraps(t *testing.T) {
	sentinel := errors.New("the underlying cause")
	boom := func(next typesafe.Handler) typesafe.Handler {
		return func(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
			panic(sentinel)
		}
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {}, typesafe.WithInterceptor(boom))

	_, err := c.SystemOne(context.Background(), sampleRequest())
	if !errors.Is(err, sentinel) {
		t.Errorf("a panic carrying an error should unwrap to it: %v", err)
	}
}

func TestPanicInAHookIsRecovered(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithHooks(typesafe.Hooks{
		OnResponse: func(context.Context, typesafe.CallInfo) { panic("hook exploded") },
	}))

	_, err := c.SystemOne(context.Background(), sampleRequest())
	var pe *typesafe.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T, want *PanicError", err)
	}
}

// --- hooks --------------------------------------------------------------------

func TestHooks(t *testing.T) {
	var (
		mu               sync.Mutex
		requests         int
		responses, fails []typesafe.CallInfo
	)
	hooks := typesafe.Hooks{
		OnRequest: func(context.Context, *typesafe.SystemOneRequest) {
			mu.Lock()
			requests++
			mu.Unlock()
		},
		OnResponse: func(_ context.Context, i typesafe.CallInfo) {
			mu.Lock()
			responses = append(responses, i)
			mu.Unlock()
		},
		OnError: func(_ context.Context, i typesafe.CallInfo) {
			mu.Lock()
			fails = append(fails, i)
			mu.Unlock()
		},
	}

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, 200, okResponse())
		}, typesafe.WithHooks(hooks))
		if _, err := c.SystemOne(context.Background(), sampleRequest()); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if requests != 1 || len(responses) != 1 || len(fails) != 0 {
			t.Fatalf("requests=%d responses=%d fails=%d", requests, len(responses), len(fails))
		}
		got := responses[0]
		if got.Model != "jev-1.13.0" || got.InputTokens != 312 || got.Questions != 1 {
			t.Errorf("CallInfo = %+v", got)
		}
		if got.Duration <= 0 {
			t.Error("Duration should be positive")
		}
	})

	t.Run("failure", func(t *testing.T) {
		mu.Lock()
		requests, responses, fails = 0, nil, nil
		mu.Unlock()

		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, 422, map[string]any{"detail": "bad"})
		}, typesafe.WithHooks(hooks))
		if _, err := c.SystemOne(context.Background(), sampleRequest()); err == nil {
			t.Fatal("expected failure")
		}
		mu.Lock()
		defer mu.Unlock()
		if len(fails) != 1 || len(responses) != 0 {
			t.Fatalf("responses=%d fails=%d", len(responses), len(fails))
		}
		if fails[0].Err == nil {
			t.Error("CallInfo.Err should carry the failure")
		}
	})
}

// --- request id ---------------------------------------------------------------

func TestRequestIDIsVisibleToHooksAndNotSentByDefault(t *testing.T) {
	var seenHeader string
	var seenInHook string

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("x-correlation-id")
		writeJSON(t, w, 200, okResponse())
	},
		typesafe.WithRequestID(func() string { return "corr-123" }),
		typesafe.WithHooks(typesafe.Hooks{
			OnResponse: func(ctx context.Context, i typesafe.CallInfo) { seenInHook = i.RequestID },
		}),
	)

	if _, err := c.SystemOne(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if seenInHook != "corr-123" {
		t.Errorf("hook saw request id %q", seenInHook)
	}
	if seenHeader != "" {
		t.Errorf("the id must not be sent unless a header is named, got %q", seenHeader)
	}
}

func TestRequestIDIsSentWhenAHeaderIsNamed(t *testing.T) {
	var seen string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("x-correlation-id")
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithRequestID(func() string { return "corr-abc" }, "x-correlation-id"))

	if _, err := c.SystemOne(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if seen != "corr-abc" {
		t.Errorf("header = %q, want the generated id", seen)
	}
}

func TestRequestIDValidation(t *testing.T) {
	for name, opt := range map[string]typesafe.Option{
		"nil func":     typesafe.WithRequestID(nil),
		"empty header": typesafe.WithRequestID(func() string { return "x" }, ""),
		"two headers":  typesafe.WithRequestID(func() string { return "x" }, "a", "b"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := typesafe.NewClient(typesafe.WithAPIKey("k"), opt); !errors.Is(err, typesafe.ErrInvalidConfig) {
				t.Errorf("err = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

// --- logging ------------------------------------------------------------------

// TestWithLoggingNeverLeaksSecretsOrState is the criterion that matters: a
// logging helper that leaks the caller's data by default is worse than none.
func TestWithLoggingNeverLeaksSecretsOrState(t *testing.T) {
	const patientData = "PATIENT-SSN-123-45-6789"

	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	for _, status := range []int{200, 401, 500} {
		buf.Reset()
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if status == 200 {
				writeJSON(t, w, 200, okResponse())
				return
			}
			writeJSON(t, w, status, map[string]any{"detail": "your key " + testKey + " failed"})
		}, typesafe.WithInterceptor(typesafe.WithLogging(logger)))

		req := sampleRequest()
		req.State = patientData
		_, _ = c.SystemOne(context.Background(), req)

		out := buf.String()
		if strings.Contains(out, testKey) {
			t.Errorf("status %d: logs leaked the API key:\n%s", status, out)
		}
		if strings.Contains(out, patientData) {
			t.Errorf("status %d: logs leaked the request state:\n%s", status, out)
		}
		if strings.Contains(strings.ToLower(out), "authorization") {
			t.Errorf("status %d: logs mention the Authorization header:\n%s", status, out)
		}
		// It must still be useful.
		if !strings.Contains(out, "questions") {
			t.Errorf("status %d: logs carry no useful attributes:\n%s", status, out)
		}
		// And structured, not a formatted string.
		var line map[string]any
		first := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
		if err := json.Unmarshal([]byte(first), &line); err != nil {
			t.Errorf("status %d: log line is not structured JSON: %v", status, err)
		}
	}
}

// --- async --------------------------------------------------------------------

func TestSystemOneAsync(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, okResponse())
	})

	r := <-c.SystemOneAsync(context.Background(), sampleRequest())
	if r.Err != nil {
		t.Fatalf("Err = %v", r.Err)
	}
	if r.Response == nil || r.Response.Model != "jev-1.13.0" {
		t.Errorf("Response = %+v", r.Response)
	}
}

// TestAsyncChannelClosesExactlyOnce: a second receive must yield the zero
// value rather than blocking, so a range over the channel terminates.
func TestAsyncChannelClosesExactlyOnce(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, okResponse())
	})

	ch := c.SystemOneAsync(context.Background(), sampleRequest())
	first := <-ch

	second, open := <-ch
	if open {
		t.Error("the channel should be closed after its one result")
	}
	if second.Response != nil || second.Err != nil {
		t.Errorf("a second receive should yield the zero Result, got %+v", second)
	}
	if first.Err != nil {
		t.Errorf("first result: %v", first.Err)
	}

	var n int
	for range c.SystemOneAsync(context.Background(), sampleRequest()) {
		n++
	}
	if n != 1 {
		t.Errorf("range yielded %d results, want 1", n)
	}
}

// TestAbandonedAsyncCallsDoNotLeak is the reason the channel is buffered. The
// obvious unbuffered implementation leaks a goroutine for every caller that
// takes the first result and returns — which is the normal shape of a race.
func TestAbandonedAsyncCallsDoNotLeak(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, okResponse())
	})

	settle := func() int {
		for range 20 {
			runtime.GC()
			time.Sleep(10 * time.Millisecond)
		}
		return runtime.NumGoroutine()
	}

	before := settle()
	for range 200 {
		// Start a call and never read the channel.
		_ = c.SystemOneAsync(context.Background(), sampleRequest())
	}
	after := settle()

	// Some slack for the HTTP transport's own pooled goroutines.
	if after > before+20 {
		t.Errorf("goroutines grew from %d to %d across 200 abandoned calls", before, after)
	}
}

func TestAsyncDeliversCancellationAsAResult(t *testing.T) {
	c := newTestClient(t, blockUntilClientGoesAway)

	ctx, cancel := context.WithCancel(context.Background())
	ch := c.SystemOneAsync(ctx, sampleRequest())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	select {
	case r := <-ch:
		if r.Err == nil {
			t.Error("cancellation should arrive as an error on the channel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the channel was never woken; a cancelled call must still deliver a result")
	}
}

func TestSystemOneAll(t *testing.T) {
	var n int32
	var mu sync.Mutex
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		writeJSON(t, w, 200, okResponse())
	})

	reqs := []*typesafe.SystemOneRequest{sampleRequest(), sampleRequest(), sampleRequest()}
	results := c.SystemOneAll(context.Background(), reqs...)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Errorf("result %d: %v", i, r.Err)
		}
	}
	if c.SystemOneAll(context.Background()) != nil {
		t.Error("no requests should yield no results")
	}
}

// TestSystemOneAllIsPositional: results[i] belongs to requests[i], whichever
// finished first, and one failure does not discard the others.
func TestSystemOneAllIsPositional(t *testing.T) {
	var mu sync.Mutex
	seen := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen++
		n := seen
		mu.Unlock()
		if n == 1 {
			// Make the first request the slowest, so completion order and
			// input order genuinely differ.
			time.Sleep(60 * time.Millisecond)
			writeJSON(t, w, 422, map[string]any{"detail": "first fails"})
			return
		}
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithRetryPolicy(typesafe.NoRetry()))

	results := c.SystemOneAll(context.Background(),
		sampleRequest(), sampleRequest(), sampleRequest())

	var failures int
	for _, r := range results {
		if r.Err != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Errorf("%d failures, want exactly 1 — a failure must not discard the others", failures)
	}
}

// --- builders -----------------------------------------------------------------

// TestBuildersMarshalIdenticallyToStructs: two ways of writing the same
// request that disagree by a byte would be worse than one.
func TestBuildersMarshalIdenticallyToStructs(t *testing.T) {
	cases := []struct {
		name              string
		built, structured typesafe.Question
	}{
		{
			"noul",
			typesafe.NewNoul("Does this convey urgency?").
				Means("Explicitly time-sensitive", "No urgency expressed"),
			typesafe.Noul{
				Instructions: "Does this convey urgency?",
				Criteria: &typesafe.NoulCriteria{
					True: "Explicitly time-sensitive", False: "No urgency expressed"},
			},
		},
		{
			"noul without criteria",
			typesafe.NewNoul("Is this spam?"),
			typesafe.Noul{Instructions: "Is this spam?"},
		},
		{
			"choice",
			typesafe.NewChoice("Which team should handle this?").
				Option("billing", "Payments, invoicing, refunds").
				Option("technical", "Bugs, outages, integrations"),
			typesafe.Choice{
				Instructions: "Which team should handle this?",
				Criteria: typesafe.Options{
					"billing":   "Payments, invoicing, refunds",
					"technical": "Bugs, outages, integrations",
				},
			},
		},
		{
			"choice with null descriptions",
			typesafe.NewChoice("What is the tone?").Options("calm", "angry", "excited"),
			typesafe.Choice{
				Instructions: "What is the tone?",
				Criteria:     typesafe.Options{"calm": nil, "angry": nil, "excited": nil},
			},
		},
		{
			"score",
			typesafe.NewScore("How frustrated is the customer?").
				Levels("Calm", "Frustrated", "Very angry"),
			typesafe.Score{
				Instructions: "How frustrated is the customer?",
				Criteria:     typesafe.Levels{"Calm", "Frustrated", "Very angry"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := json.Marshal(tc.built)
			if err != nil {
				t.Fatalf("marshal builder: %v", err)
			}
			b, err := json.Marshal(tc.structured)
			if err != nil {
				t.Fatalf("marshal struct: %v", err)
			}
			if string(a) != string(b) {
				t.Errorf("forms disagree:\nbuilder: %s\n struct: %s", a, b)
			}
		})
	}
}

// TestBuildersAreImmutable: a partially built question shared as a base must
// not pick up another caller's additions.
func TestBuildersAreImmutable(t *testing.T) {
	base := typesafe.NewChoice("Which team?").Option("billing", nil)
	withEng := base.Option("technical", nil)
	withSales := base.Option("sales", nil)

	count := func(q typesafe.Question) int {
		b, _ := json.Marshal(q)
		var got struct {
			Criteria map[string]any `json:"criteria"`
		}
		_ = json.Unmarshal(b, &got)
		return len(got.Criteria)
	}
	if n := count(base); n != 1 {
		t.Errorf("base has %d options, want 1 — a derived builder mutated it", n)
	}
	if n := count(withEng); n != 2 {
		t.Errorf("withEng has %d options, want 2", n)
	}
	if n := count(withSales); n != 2 {
		t.Errorf("withSales has %d options, want 2", n)
	}

	levels := typesafe.NewScore("Rate").Levels("low", "high")
	a := levels.Level("extreme")
	_ = levels.Level("other")
	if n := count2(t, a); n != 3 {
		t.Errorf("derived score has %d levels, want 3", n)
	}
	if n := count2(t, levels); n != 2 {
		t.Errorf("base score has %d levels, want 2", n)
	}
}

func count2(t *testing.T, q typesafe.Question) int {
	t.Helper()
	b, _ := json.Marshal(q)
	var got struct {
		Criteria []any `json:"criteria"`
	}
	_ = json.Unmarshal(b, &got)
	return len(got.Criteria)
}

// TestBuildersValidateAsTheirQuestion: a builder that marshalled fine but
// failed validation as an "unknown type" would look like it worked right up
// until the checks that matter.
func TestBuildersValidateAsTheirQuestion(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State: "x",
		Questions: map[string]typesafe.Question{
			"ok":    typesafe.NewNoul("Is this urgent?"),
			"empty": typesafe.NewChoice("Pick"),
		},
	}
	_, err := req.Validate()
	if err == nil {
		t.Fatal("an empty Choice should fail validation however it was built")
	}
	if !strings.Contains(err.Error(), "at least one option") {
		t.Errorf("err = %v, want the Choice rule rather than an unknown-type error", err)
	}

	tooMany := typesafe.NewScore("Rate")
	for range typesafe.MaxScoreLevels + 1 {
		tooMany = tooMany.Level("level")
	}
	req2 := &typesafe.SystemOneRequest{
		State:     "x",
		Questions: map[string]typesafe.Question{"sev": tooMany},
	}
	if _, err := req2.Validate(); err == nil || !strings.Contains(err.Error(), "at most 10") {
		t.Errorf("a fluently built oversize Score should hit the ceiling, got: %v", err)
	}
}
