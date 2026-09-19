package typesafeprom_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/typesafeprom"
)

// --- harness -----------------------------------------------------------------

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
		State: "Help!",
		Model: "jev-latest",
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.Noul{Instructions: "Urgent?"},
			"team": typesafe.Choice{
				Instructions: "Which team?",
				Criteria:     typesafe.Options{"billing": "money", "technical": "bugs"},
			},
		},
	}
}

// families registers m in a fresh registry and returns what it exposes.
func families(t *testing.T, m *typesafeprom.Metrics) map[string]*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(m); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, f := range got {
		out[f.GetName()] = f
	}
	return out
}

// labeled finds the metric carrying every given label pair.
func labeled(f *dto.MetricFamily, want map[string]string) *dto.Metric {
	if f == nil {
		return nil
	}
metrics:
	for _, m := range f.GetMetric() {
		have := map[string]string{}
		for _, lp := range m.GetLabel() {
			have[lp.GetName()] = lp.GetValue()
		}
		for k, v := range want {
			if have[k] != v {
				continue metrics
			}
		}
		return m
	}
	return nil
}

func clientWith(t *testing.T, url string, m *typesafeprom.Metrics, opts ...typesafe.Option) *typesafe.Client {
	t.Helper()
	all := append([]typesafe.Option{
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(url),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		typesafe.WithInterceptor(m.Interceptor()),
		typesafe.WithRetryObserver(m.RetryObserver()),
	}, opts...)
	c, err := typesafe.NewClient(all...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// --- registration ------------------------------------------------------------

// The exit criterion: every collector is exposed, and the buckets are the ones
// the latency range calls for.
func TestEveryCollectorIsExposed(t *testing.T) {
	m := typesafeprom.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	client := clientWith(t, srv.URL, m)
	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	// Touch the collectors a call alone does not reach.
	m.BatchCallback()(typesafe.ItemResult{Index: 0})
	m.BatchCallback()(typesafe.ItemResult{Index: 1, Err: errors.New("boom")})
	m.CacheObserver()(true, "memory", false, nil)
	m.RetryObserver()(context.Background(), typesafe.AttemptInfo{Status: 429})

	fams := families(t, m)
	for _, name := range []string{
		"typesafe_request_duration_seconds",
		"typesafe_answer_confidence",
		"typesafe_retries_total",
		"typesafe_questions_total",
		"typesafe_tokens_total",
		"typesafe_batch_items_total",
		"typesafe_requests_in_flight",
		"typesafe_cache_events_total",
	} {
		if _, ok := fams[name]; !ok {
			t.Errorf("%s was not exposed", name)
		}
	}

	// Every metric must carry help text; an unexplained panel is a panel
	// nobody trusts.
	for name, f := range fams {
		if strings.TrimSpace(f.GetHelp()) == "" {
			t.Errorf("%s has no help text", name)
		}
	}
}

func TestDurationBucketsCoverTheDocumentedRange(t *testing.T) {
	m := typesafeprom.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	client := clientWith(t, srv.URL, m)
	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	f := families(t, m)["typesafe_request_duration_seconds"]
	metric := labeled(f, map[string]string{"model": "jev-1.13.0", "outcome": "ok"})
	if metric == nil {
		t.Fatal("no duration observation for a successful call")
	}

	// The documented range is 70–500ms. At least four bucket boundaries must
	// fall inside it, or the histogram cannot distinguish a normal call from
	// a slow one.
	var inRange int
	for _, b := range metric.GetHistogram().GetBucket() {
		if ub := b.GetUpperBound(); ub >= 0.07 && ub <= 0.5 {
			inRange++
		}
	}
	if inRange < 4 {
		t.Errorf("%d bucket boundaries fall in the documented 70–500ms range, want at least 4", inRange)
	}
	if metric.GetHistogram().GetSampleCount() != 1 {
		t.Errorf("sample count = %d, want 1", metric.GetHistogram().GetSampleCount())
	}
}

// --- what gets recorded ------------------------------------------------------

func TestSuccessfulCallRecordsUsageQuestionsAndConfidence(t *testing.T) {
	m := typesafeprom.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	client := clientWith(t, srv.URL, m)
	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	fams := families(t, m)

	// Tokens are labeled by kind, so a cost panel can sum input alone.
	in := labeled(fams["typesafe_tokens_total"], map[string]string{"kind": "input", "model": "jev-1.13.0"})
	out := labeled(fams["typesafe_tokens_total"], map[string]string{"kind": "output", "model": "jev-1.13.0"})
	if in == nil || in.GetCounter().GetValue() != 312 {
		t.Errorf("input tokens = %v, want 312", in.GetCounter().GetValue())
	}
	if out == nil || out.GetCounter().GetValue() != 48 {
		t.Errorf("output tokens = %v, want 48", out.GetCounter().GetValue())
	}

	// Questions counted by primitive.
	noul := labeled(fams["typesafe_questions_total"], map[string]string{"type": "noul"})
	choice := labeled(fams["typesafe_questions_total"], map[string]string{"type": "choice"})
	if noul == nil || noul.GetCounter().GetValue() != 1 {
		t.Error("noul question not counted")
	}
	if choice == nil || choice.GetCounter().GetValue() != 1 {
		t.Error("choice question not counted")
	}

	// Confidence: a Choice contributes its confidence, a Noul its probability.
	conf := labeled(fams["typesafe_answer_confidence"], map[string]string{
		"question_type": "choice", "question_id": "team",
	})
	if conf == nil {
		t.Fatal("no confidence observation for the choice answer")
	}
	if got := conf.GetHistogram().GetSampleSum(); got != 0.81 {
		t.Errorf("choice confidence sum = %v, want 0.81", got)
	}
	noulConf := labeled(fams["typesafe_answer_confidence"], map[string]string{
		"question_type": "noul", "question_id": "is_urgent",
	})
	if noulConf == nil {
		t.Fatal("no observation for the noul answer")
	}
	if got := noulConf.GetHistogram().GetSampleSum(); got != 0.92 {
		t.Errorf("noul observation = %v, want its probability 0.92", got)
	}
}

// The model label must come from the response, not the requested alias — or an
// alias move hides inside one series.
func TestModelLabelComesFromTheResponse(t *testing.T) {
	m := typesafeprom.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	client := clientWith(t, srv.URL, m)
	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	f := families(t, m)["typesafe_request_duration_seconds"]
	if labeled(f, map[string]string{"model": "jev-1.13.0"}) == nil {
		t.Error("the duration series is not labeled with the resolved model")
	}
	if labeled(f, map[string]string{"model": "jev-latest"}) != nil {
		t.Error("the duration series is labeled with the requested alias")
	}
}

func TestRetriesAreCountedByStatus(t *testing.T) {
	m := typesafeprom.New()
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

	client := clientWith(t, srv.URL, m, typesafe.WithRetryPolicy(typesafe.RetryPolicy{
		MaxRetries: 3, BackoffInitial: time.Millisecond, BackoffMax: time.Millisecond,
		HTTPStatuses: map[int]bool{429: true}, Timeout: 10 * time.Second,
	}))
	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	got := labeled(families(t, m)["typesafe_retries_total"], map[string]string{"status": "429"})
	if got == nil || got.GetCounter().GetValue() != 2 {
		t.Errorf("429 retries = %v, want 2", got.GetCounter().GetValue())
	}
}

// --- error classification ----------------------------------------------------

// Labels must be bounded. A class built from the error message would put a
// request id in the label set and create a series per failure.
func TestErrorClassIsBounded(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, "none"},
		{&typesafe.RateLimitError{}, "rate_limit"},
		{&typesafe.OverloadedError{}, "overloaded"},
		{&typesafe.AuthenticationError{}, "authentication"},
		{&typesafe.PermissionDeniedError{}, "permission_denied"},
		{&typesafe.NotFoundError{}, "not_found"},
		{&typesafe.InternalServerError{}, "server_error"},
		{&typesafe.ConnectionError{}, "connection"},
		{&typesafe.TimeoutError{}, "timeout"},
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("something nobody predicted"), "other"},
	}
	for _, tc := range cases {
		if got := typesafeprom.ErrorClass(tc.err); got != tc.want {
			t.Errorf("ErrorClass(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}

	// The message never leaks into the label.
	noisy := errors.New("request req_abc123 failed for user 42 at 2026-01-01")
	if got := typesafeprom.ErrorClass(noisy); strings.Contains(got, "req_abc") {
		t.Errorf("ErrorClass leaked the message into the label: %q", got)
	}
}

func TestFailedCallIsCountedAndNotCountedAsUsage(t *testing.T) {
	m := typesafeprom.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": "bad key"})
	}))
	defer srv.Close()

	client := clientWith(t, srv.URL, m)
	if _, err := client.SystemOne(context.Background(), request()); err == nil {
		t.Fatal("expected an error")
	}
	fams := families(t, m)

	got := labeled(fams["typesafe_errors_total"], map[string]string{"class": "authentication"})
	if got == nil || got.GetCounter().GetValue() != 1 {
		t.Error("the authentication failure was not counted")
	}
	if labeled(fams["typesafe_tokens_total"], map[string]string{"kind": "input"}) != nil {
		t.Error("a failed call must not contribute usage")
	}
	if labeled(fams["typesafe_request_duration_seconds"], map[string]string{"outcome": "error"}) == nil {
		t.Error("a failed call must still contribute a duration observation")
	}
}

// --- batch and cache ---------------------------------------------------------

func TestBatchOutcomesAreCounted(t *testing.T) {
	m := typesafeprom.New()
	cb := m.BatchCallback()
	for i := 0; i < 7; i++ {
		cb(typesafe.ItemResult{Index: i})
	}
	for i := 0; i < 3; i++ {
		cb(typesafe.ItemResult{Index: i, Err: errors.New("boom")})
	}
	fams := families(t, m)
	ok := labeled(fams["typesafe_batch_items_total"], map[string]string{"outcome": "ok"})
	bad := labeled(fams["typesafe_batch_items_total"], map[string]string{"outcome": "error"})
	if ok == nil || ok.GetCounter().GetValue() != 7 {
		t.Errorf("ok items = %v, want 7", ok.GetCounter().GetValue())
	}
	if bad == nil || bad.GetCounter().GetValue() != 3 {
		t.Errorf("failed items = %v, want 3", bad.GetCounter().GetValue())
	}
}

func TestCacheEventsAreCounted(t *testing.T) {
	m := typesafeprom.New()
	obs := m.CacheObserver()
	obs(true, "memory", false, nil)
	obs(true, "disk", false, nil)
	obs(false, "", false, nil)
	obs(false, "", true, nil) // an alias move
	obs(false, "", false, errors.New("corrupt"))

	fams := families(t, m)
	for label, want := range map[string]float64{
		"hit_memory":  1,
		"hit_disk":    1,
		"miss":        1,
		"alias_moved": 1,
		"error":       1,
	} {
		got := labeled(fams["typesafe_cache_events_total"], map[string]string{"result": label})
		if got == nil || got.GetCounter().GetValue() != want {
			t.Errorf("cache result %q = %v, want %v", label, got.GetCounter().GetValue(), want)
		}
	}
}

// --- options -----------------------------------------------------------------

func TestQuestionIDLabelCanBeTurnedOff(t *testing.T) {
	m := typesafeprom.New(typesafeprom.WithQuestionIDLabel(false))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	client := clientWith(t, srv.URL, m)
	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	f := families(t, m)["typesafe_answer_confidence"]
	for _, metric := range f.GetMetric() {
		for _, lp := range metric.GetLabel() {
			if lp.GetName() == "question_id" && lp.GetValue() != "" {
				t.Errorf("question_id = %q despite the label being turned off", lp.GetValue())
			}
		}
	}
	// The observations are still recorded, just not broken out by id.
	if labeled(f, map[string]string{"question_type": "choice"}) == nil {
		t.Error("confidence observations were lost along with the label")
	}
}

func TestConstLabelsApplyToEveryMetric(t *testing.T) {
	m := typesafeprom.New(typesafeprom.WithConstLabels(prometheus.Labels{"env": "staging"}))
	m.BatchCallback()(typesafe.ItemResult{})
	m.RetryObserver()(context.Background(), typesafe.AttemptInfo{Status: 500})

	for name, f := range families(t, m) {
		for _, metric := range f.GetMetric() {
			var found bool
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "env" && lp.GetValue() == "staging" {
					found = true
				}
			}
			if !found {
				t.Errorf("%s is missing the constant label", name)
			}
		}
	}
}

func TestCustomBuckets(t *testing.T) {
	m := typesafeprom.New(
		typesafeprom.WithDurationBuckets([]float64{1, 2, 3}),
		typesafeprom.WithConfidenceBuckets([]float64{0.5}),
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okBody())
	}))
	defer srv.Close()

	client := clientWith(t, srv.URL, m)
	if _, err := client.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	f := families(t, m)["typesafe_request_duration_seconds"]
	metric := labeled(f, map[string]string{"outcome": "ok"})
	if metric == nil {
		t.Fatal("no observation")
	}
	if n := len(metric.GetHistogram().GetBucket()); n != 3 {
		t.Errorf("%d buckets, want the 3 that were configured", n)
	}

	// An empty override is ignored rather than producing a histogram with no
	// buckets, which would silently stop answering quantile queries.
	m2 := typesafeprom.New(typesafeprom.WithDurationBuckets(nil))
	client2 := clientWith(t, srv.URL, m2)
	if _, err := client2.SystemOne(context.Background(), request()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	f2 := families(t, m2)["typesafe_request_duration_seconds"]
	metric2 := labeled(f2, map[string]string{"outcome": "ok"})
	if n := len(metric2.GetHistogram().GetBucket()); n != len(typesafeprom.DefaultDurationBuckets) {
		t.Errorf("%d buckets after a nil override, want the defaults", n)
	}
}

// Registering twice in one registry must fail cleanly rather than panic, which
// is what tells a caller they wired it up twice.
func TestDoubleRegistrationIsAnError(t *testing.T) {
	m := typesafeprom.New()
	reg := prometheus.NewRegistry()
	if err := reg.Register(m); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := reg.Register(m); err == nil {
		t.Error("registering the same collector twice should fail")
	}
}
