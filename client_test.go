package typesafe_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
)

const testKey = "sk-test-0123456789abcdef-SECRET"

// newTestClient starts a server running h and returns a client aimed at it.
func newTestClient(t *testing.T, h http.HandlerFunc, opts ...typesafe.Option) *typesafe.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Retries off by default here: these tests assert single-attempt
	// behavior. Any test that wants retrying passes WithRetryPolicy itself,
	// and options are applied in order so a later one wins.
	all := append([]typesafe.Option{
		typesafe.WithAPIKey(testKey),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
	}, opts...)

	c, err := typesafe.NewClient(all...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func sampleRequest() *typesafe.SystemOneRequest {
	return &typesafe.SystemOneRequest{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.RawQuestion{
				"type":         "noul",
				"instructions": "Does this convey urgency?",
			},
		},
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(typesafe.RequestIDHeader, "req_test_abc123")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

func okResponse() map[string]any {
	return map[string]any{
		"model":   "jev-1.13.0",
		"answers": map[string]any{"is_urgent": map[string]any{"type": "noul", "noul": 0.92}},
		"usage":   map[string]any{"input_tokens": 312, "output_tokens": 48},
	}
}

// --- configuration -----------------------------------------------------------

func TestNewClientRequiresAPIKey(t *testing.T) {
	t.Setenv(typesafe.EnvAPIKey, "")
	_, err := typesafe.NewClient()
	if !errors.Is(err, typesafe.ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
	if !strings.Contains(err.Error(), typesafe.EnvAPIKey) {
		t.Errorf("error should name the env var, got: %v", err)
	}
}

func TestConfigResolutionOrder(t *testing.T) {
	t.Setenv(typesafe.EnvAPIKey, "from-env")
	t.Setenv(typesafe.EnvBaseURL, "https://env.example.com")
	t.Setenv(typesafe.EnvDefaultModel, "jev-from-env")

	t.Run("env is used when no option is given", func(t *testing.T) {
		c, err := typesafe.NewClient()
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if got := c.BaseURL(); got != "https://env.example.com" {
			t.Errorf("BaseURL = %q, want the env value", got)
		}
		if got := c.DefaultModel(); got != "jev-from-env" {
			t.Errorf("DefaultModel = %q, want the env value", got)
		}
	})

	t.Run("options beat env", func(t *testing.T) {
		c, err := typesafe.NewClient(
			typesafe.WithBaseURL("https://opt.example.com"),
			typesafe.WithDefaultModel("jev-from-option"),
		)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if got := c.BaseURL(); got != "https://opt.example.com" {
			t.Errorf("BaseURL = %q, want the option value", got)
		}
		if got := c.DefaultModel(); got != "jev-from-option" {
			t.Errorf("DefaultModel = %q, want the option value", got)
		}
	})
}

func TestDefaultsMatchOfficialSDKs(t *testing.T) {
	t.Setenv(typesafe.EnvAPIKey, "k")
	t.Setenv(typesafe.EnvBaseURL, "")
	t.Setenv(typesafe.EnvDefaultModel, "")

	c, err := typesafe.NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := c.BaseURL(); got != typesafe.DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", got, typesafe.DefaultBaseURL)
	}
	if got := c.DefaultModel(); got != typesafe.DefaultModel {
		t.Errorf("DefaultModel = %q, want %q", got, typesafe.DefaultModel)
	}
	if typesafe.DefaultTimeout != 10*time.Second {
		t.Errorf("DefaultTimeout = %v, want 10s to match the official SDKs", typesafe.DefaultTimeout)
	}
}

func TestInvalidOptions(t *testing.T) {
	cases := map[string]typesafe.Option{
		"empty key":          typesafe.WithAPIKey("  "),
		"bad url":            typesafe.WithBaseURL("://nope"),
		"non-http scheme":    typesafe.WithBaseURL("ftp://example.com"),
		"no host":            typesafe.WithBaseURL("https://"),
		"zero timeout":       typesafe.WithTimeout(0),
		"negative timeout":   typesafe.WithTimeout(-time.Second),
		"nil http client":    typesafe.WithHTTPClient(nil),
		"nil logger":         typesafe.WithLogger(nil),
		"empty model":        typesafe.WithDefaultModel(""),
		"empty ua suffix":    typesafe.WithUserAgentSuffix(" "),
		"newline in ua":      typesafe.WithUserAgentSuffix("a\r\nb"),
		"authorization hdr":  typesafe.WithHeader("Authorization", "Bearer nope"),
		"authorization case": typesafe.WithHeader("authorization", "Bearer nope"),
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := typesafe.NewClient(typesafe.WithAPIKey("k"), opt)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, typesafe.ErrInvalidConfig) {
				t.Errorf("err = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

// TestSuppliedClientKeepsATimeout guards a quiet foot-gun: passing your own
// *http.Client must not silently remove the timeout.
//
// Asserted by behavior, not by inspection. http.Client.Timeout is enforced in
// the transport and is invisible to the server's own request context, so a
// handler cannot observe it — the only honest check is that a slow server
// actually produces a timeout.
func TestSuppliedClientKeepsATimeout(t *testing.T) {
	c := newTestClient(t, blockUntilClientGoesAway,
		typesafe.WithHTTPClient(&http.Client{}), // no Timeout set
		typesafe.WithTimeout(50*time.Millisecond))

	start := time.Now()
	_, err := c.SystemOne(context.Background(), sampleRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a supplied http.Client with no Timeout left the request unbounded")
	}
	var te *typesafe.TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %T (%v), want *TimeoutError", err, err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v; the 50ms timeout was not applied", elapsed)
	}
}

// --- request shape -----------------------------------------------------------

func TestRequestIsWellFormed(t *testing.T) {
	var (
		gotAuth, gotUA, gotCT, gotAccept, gotCustom string
		gotBody                                     map[string]any
		gotMethod, gotPath                          string
		gotLen                                      int64
	)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotCT = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotCustom = r.Header.Get("X-Trace")
		gotLen = r.ContentLength
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithHeader("X-Trace", "abc"), typesafe.WithUserAgentSuffix("myapp/2.1"))

	if _, err := c.SystemOne(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != typesafe.SystemOnePath {
		t.Errorf("got %s %s, want POST %s", gotMethod, gotPath, typesafe.SystemOnePath)
	}
	if gotAuth != "Bearer "+testKey {
		t.Error("Authorization header is not the bearer key")
	}
	if gotCT != "application/json" || gotAccept != "application/json" {
		t.Errorf("Content-Type = %q, Accept = %q", gotCT, gotAccept)
	}
	if gotCustom != "abc" {
		t.Errorf("X-Trace = %q, want the configured value", gotCustom)
	}
	if !strings.HasPrefix(gotUA, "typesafe-go/") || !strings.HasSuffix(gotUA, "myapp/2.1") {
		t.Errorf("User-Agent = %q, want the SDK identity first and the suffix last", gotUA)
	}
	if gotLen <= 0 {
		t.Error("Content-Length was not set; Phase 4 retries need identical, measurable bodies")
	}
	// The model must be defaulted onto the wire even though the caller left it empty.
	if gotBody["model"] != typesafe.DefaultModel {
		t.Errorf("wire model = %v, want %q", gotBody["model"], typesafe.DefaultModel)
	}
}

// TestDefaultingDoesNotMutateCallerRequest: filling in the model must not be
// visible in the caller's struct, or a reused request silently pins a model.
func TestDefaultingDoesNotMutateCallerRequest(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, okResponse())
	})
	req := sampleRequest()
	if _, err := c.SystemOne(context.Background(), req); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if req.Model != "" {
		t.Errorf("caller's request was mutated: Model = %q", req.Model)
	}
}

func TestClientSideValidation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached the server; it should have failed client-side")
	})
	cases := map[string]*typesafe.SystemOneRequest{
		"nil request":   nil,
		"no questions":  {State: "x", Questions: map[string]typesafe.Question{}},
		"nil questions": {State: "x"},
		"no state":      {Questions: map[string]typesafe.Question{"q": typesafe.RawQuestion{"type": "noul"}}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := c.SystemOne(context.Background(), req); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// --- responses ---------------------------------------------------------------

func TestSuccessfulResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, okResponse())
	})
	resp, err := c.SystemOne(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if resp.Model != "jev-1.13.0" {
		t.Errorf("Model = %q, want the versioned id the server reported", resp.Model)
	}
	if resp.Usage.InputTokens != 312 || resp.Usage.OutputTokens != 48 {
		t.Errorf("Usage = %+v", resp.Usage)
	}
	raw, ok := resp.Answers["is_urgent"]
	if !ok {
		t.Fatal("answer is_urgent missing")
	}
	var a struct {
		Type string  `json:"type"`
		Noul float64 `json:"noul"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if a.Type != "noul" || a.Noul != 0.92 {
		t.Errorf("answer = %+v", a)
	}
}

func TestErrorStatuses(t *testing.T) {
	cases := []struct {
		status   int
		sentinel error
		as       func(error) bool
	}{
		{400, typesafe.ErrBadRequest, func(e error) bool {
			var t *typesafe.BadRequestError
			return errors.As(e, &t)
		}},
		{401, typesafe.ErrAuthentication, func(e error) bool {
			var t *typesafe.AuthenticationError
			return errors.As(e, &t)
		}},
		{403, typesafe.ErrPermissionDenied, func(e error) bool {
			var t *typesafe.PermissionDeniedError
			return errors.As(e, &t)
		}},
		{404, typesafe.ErrNotFound, func(e error) bool {
			var t *typesafe.NotFoundError
			return errors.As(e, &t)
		}},
		{422, typesafe.ErrInvalidRequest, func(e error) bool {
			var t *typesafe.UnprocessableEntityError
			return errors.As(e, &t)
		}},
		{429, typesafe.ErrRateLimit, func(e error) bool {
			var t *typesafe.RateLimitError
			return errors.As(e, &t)
		}},
		{500, typesafe.ErrInternalServer, func(e error) bool {
			var t *typesafe.InternalServerError
			return errors.As(e, &t)
		}},
		{503, typesafe.ErrInternalServer, func(e error) bool {
			var t *typesafe.InternalServerError
			return errors.As(e, &t)
		}},
		{typesafe.StatusOverloaded, typesafe.ErrOverloaded, func(e error) bool {
			var t *typesafe.OverloadedError
			return errors.As(e, &t)
		}},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status)+"_"+itoa(tc.status), func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, tc.status, map[string]any{"detail": "nope"})
			})
			_, err := c.SystemOne(context.Background(), sampleRequest())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !tc.as(err) {
				t.Errorf("errors.As did not match the expected concrete type; got %T", err)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(%v) failed", tc.sentinel)
			}

			// Every status-specific error must also unwrap to *APIError.
			var api *typesafe.APIError
			if !errors.As(err, &api) {
				t.Fatalf("errors.As(*APIError) failed for %T", err)
			}
			if api.Status != tc.status {
				t.Errorf("APIError.Status = %d, want %d", api.Status, tc.status)
			}
			if api.RequestID != "req_test_abc123" {
				t.Errorf("RequestID = %q, want the header value", api.RequestID)
			}
			if api.Endpoint == "" {
				t.Error("Endpoint is empty")
			}
		})
	}
}

// TestUnprocessableEntityDetail is the payoff for parsing the documented 422
// envelope: the caller learns which field was rejected, not just that one was.
func TestUnprocessableEntityDetail(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 422, map[string]any{
			"detail": []map[string]any{{
				"loc":   []any{"body", "questions", "frustration", "criteria"},
				"msg":   "Field required",
				"type":  "missing",
				"input": nil,
			}, {
				"loc":  []any{"body", "questions", "q", "criteria", 0},
				"msg":  "Input should be a valid string",
				"type": "string_type",
			}},
		})
	})
	_, err := c.SystemOne(context.Background(), sampleRequest())

	var ue *typesafe.UnprocessableEntityError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %T, want *UnprocessableEntityError", err)
	}
	if len(ue.Detail) != 2 {
		t.Fatalf("parsed %d details, want 2", len(ue.Detail))
	}
	if got, want := ue.Detail[0].Path(), "body.questions.frustration.criteria"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
	if got, want := ue.Detail[1].Path(), "body.questions.q.criteria[0]"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
	if ue.Detail[0].Type != "missing" {
		t.Errorf("Type = %q", ue.Detail[0].Type)
	}
	// The message should surface the offending field, not just a status.
	if !strings.Contains(err.Error(), "body.questions.frustration.criteria") {
		t.Errorf("Error() should name the rejected field, got: %v", err)
	}
}

// TestObjectDetailShape covers the second "detail" shape. The API returns an
// array of field errors for schema failures (422) but a single object with
// error_type and message for everything else (400, 401). Neither shape is
// published; both were captured from the live API. Handling only one loses the
// other silently.
func TestObjectDetailShape(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      any
		errorType string
		message   string
	}{
		{
			name:   "401 authentication_error",
			status: 401,
			body: map[string]any{"detail": map[string]any{
				"error_type": "authentication_error",
				"message":    "Cannot authenticate with the server. Please check your API key and try again.",
			}},
			errorType: "authentication_error",
			message:   "Cannot authenticate with the server. Please check your API key and try again.",
		},
		{
			name:   "400 api_usage_error",
			status: 400,
			body: map[string]any{"detail": map[string]any{
				"error_type": "api_usage_error",
				"message":    "Unknown model: jev-does-not-exist",
			}},
			errorType: "api_usage_error",
			message:   "Unknown model: jev-does-not-exist",
		},
		{
			name:      "400 too many score levels",
			status:    400,
			body:      map[string]any{"detail": "Too many score levels. Must have at most 10 levels."},
			errorType: "",
			message:   "Too many score levels. Must have at most 10 levels.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, tc.status, tc.body)
			})
			_, err := c.SystemOne(context.Background(), sampleRequest())

			var api *typesafe.APIError
			if !errors.As(err, &api) {
				t.Fatalf("err = %T, want an *APIError", err)
			}
			if api.Reason == nil {
				t.Fatal("Reason is nil; the object detail shape was not parsed")
			}
			if api.Reason.ErrorType != tc.errorType {
				t.Errorf("ErrorType = %q, want %q", api.Reason.ErrorType, tc.errorType)
			}
			if api.Reason.Message != tc.message {
				t.Errorf("Message = %q, want %q", api.Reason.Message, tc.message)
			}
			if len(api.Detail) != 0 {
				t.Errorf("Detail should be empty for the object shape, got %d entries", len(api.Detail))
			}
			// The server's explanation must reach the user.
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("Error() should include the server message, got: %v", err)
			}
		})
	}
}

// TestScoreLevelCeilingIsReported is a regression guard for a boundary that no
// published source mentions: more than ten Score levels is a 400.
func TestScoreLevelCeilingIsReported(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 400, map[string]any{
			"detail": "Too many score levels. Must have at most 10 levels."})
	})
	_, err := c.SystemOne(context.Background(), sampleRequest())

	var bre *typesafe.BadRequestError
	if !errors.As(err, &bre) {
		t.Fatalf("err = %T, want *BadRequestError", err)
	}
	if !errors.Is(err, typesafe.ErrBadRequest) {
		t.Error("errors.Is(ErrBadRequest) failed")
	}
	if !strings.Contains(err.Error(), "at most 10 levels") {
		t.Errorf("the ceiling should be visible in the message, got: %v", err)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"seconds", "3", 3 * time.Second},
		{"fractional seconds", "1.5", 1500 * time.Millisecond},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"absent", "", 0},
		{"garbage", "soon", 0},
		{"http date in the past", "Mon, 01 Jan 2001 00:00:00 GMT", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(429)
			})
			_, err := c.SystemOne(context.Background(), sampleRequest())
			var rl *typesafe.RateLimitError
			if !errors.As(err, &rl) {
				t.Fatalf("err = %T, want *RateLimitError", err)
			}
			if rl.RetryAfter != tc.want {
				t.Errorf("RetryAfter = %v, want %v", rl.RetryAfter, tc.want)
			}
		})
	}

	t.Run("http date in the future", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", time.Now().UTC().Add(30*time.Second).Format(http.TimeFormat))
			w.WriteHeader(429)
		})
		_, err := c.SystemOne(context.Background(), sampleRequest())
		var rl *typesafe.RateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("err = %T", err)
		}
		if rl.RetryAfter < 25*time.Second || rl.RetryAfter > 31*time.Second {
			t.Errorf("RetryAfter = %v, want roughly 30s", rl.RetryAfter)
		}
	})
}

func TestMalformedResponses(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"not json": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			_, _ = io.WriteString(w, "<html>gateway</html>")
		},
		"missing answers": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, 200, map[string]any{
				"model": "jev-1.13.0",
				"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
			})
		},
		"answer missing for a question": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, 200, map[string]any{
				"model":   "jev-1.13.0",
				"answers": map[string]any{"something_else": map[string]any{"type": "noul", "noul": 0.5}},
				"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
			})
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, h)
			_, err := c.SystemOne(context.Background(), sampleRequest())
			var rve *typesafe.ResponseValidationError
			if !errors.As(err, &rve) {
				t.Fatalf("err = %T (%v), want *ResponseValidationError", err, err)
			}
			if !errors.Is(err, typesafe.ErrInvalidResponse) {
				t.Error("errors.Is(ErrInvalidResponse) failed")
			}
		})
	}
}

// blockUntilClientGoesAway stalls until the client hangs up, so the client-side
// cancellation and deadline paths are the ones under test.
//
// The hard cap matters: without it, a handler that never returns keeps
// httptest.Server.Close blocked in cleanup and the whole package hangs rather
// than failing. One second is ~20x the longest deadline these tests set, so it
// never fires on a passing run, and it bounds cleanup when a client hangs up
// without the server noticing promptly.
func blockUntilClientGoesAway(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(time.Second):
	}
}

// --- transport failures ------------------------------------------------------

func TestContextCancellationIsNotATimeout(t *testing.T) {
	c := newTestClient(t, blockUntilClientGoesAway)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	_, err := c.SystemOne(ctx, sampleRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var te *typesafe.TimeoutError
	if errors.As(err, &te) {
		t.Error("deliberate cancellation must not be reported as a timeout")
	}
}

func TestDeadlineExceededIsATimeout(t *testing.T) {
	c := newTestClient(t, blockUntilClientGoesAway)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := c.SystemOne(ctx, sampleRequest())
	var te *typesafe.TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %T (%v), want *TimeoutError", err, err)
	}
	if !errors.Is(err, typesafe.ErrTimeout) {
		t.Error("errors.Is(ErrTimeout) failed")
	}
}

func TestConnectionFailure(t *testing.T) {
	// Bind, then close, so the port is almost certainly refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	c, err := typesafe.NewClient(typesafe.WithAPIKey(testKey), typesafe.WithBaseURL("http://"+addr),
		typesafe.WithRetryPolicy(typesafe.NoRetry()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.SystemOne(context.Background(), sampleRequest())
	var ce *typesafe.ConnectionError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %T (%v), want *ConnectionError", err, err)
	}
	if !errors.Is(err, typesafe.ErrConnection) {
		t.Error("errors.Is(ErrConnection) failed")
	}
}

// --- models ------------------------------------------------------------------

func TestModels(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != typesafe.ModelsPath {
			t.Errorf("got %s %s, want GET %s", r.Method, r.URL.Path, typesafe.ModelsPath)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("models request is unauthenticated")
		}
		writeJSON(t, w, 200, map[string]any{"models": []map[string]string{
			{"name": "jev-latest", "description": "Most recent stable release", "release_date": "2026-09-15"},
			{"name": "jev-preview", "description": "Most recent release", "release_date": "2026-09-15"},
		}})
	})
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].Name != "jev-latest" || models[0].ReleaseDate == "" {
		t.Errorf("model[0] = %+v", models[0])
	}
}

// --- security ----------------------------------------------------------------

// TestNoSecretInAnyErrorString is the guard that matters most: an API key that
// reaches a log aggregator is a leaked credential. No error this SDK produces
// may contain it, whatever the server sends back.
func TestNoSecretInAnyErrorString(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"401": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, 401, map[string]any{"detail": "bad key"})
		},
		"422": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, 422, map[string]any{"detail": []map[string]any{
				{"loc": []any{"body"}, "msg": "bad", "type": "x"}}})
		},
		"429": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) },
		"500": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
		"529": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(typesafe.StatusOverloaded) },
		"bad json": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			_, _ = io.WriteString(w, "{{{")
		},
		// The nastiest case, and a real bug this test caught: the server
		// echoes the credential back in its error body, which APIError.Error
		// used to interpolate verbatim into every log line.
		"echoes the key": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, 400, map[string]any{"detail": "your key " + testKey + " is wrong"})
		},
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, h)
			_, err := c.SystemOne(context.Background(), sampleRequest())
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("error string leaked the API key: %v", err)
			}
			// APIError.Body may legitimately hold whatever the server sent —
			// that is the caller's to handle — but Error() must stay clean.
			var api *typesafe.APIError
			if errors.As(err, &api) && strings.Contains(api.Endpoint, testKey) {
				t.Error("Endpoint leaked the API key")
			}
		})
	}
}

// TestNoSecretInLogs: the structured logger must never receive the key or the
// caller's state, which routinely contains personal data.
func TestNoSecretInLogs(t *testing.T) {
	var buf strings.Builder
	logger := newTestLogger(&buf)

	secretState := "PATIENT-SSN-123-45-6789"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 500, map[string]any{"detail": "boom"})
	}, typesafe.WithLogger(logger))

	req := sampleRequest()
	req.State = secretState
	_, _ = c.SystemOne(context.Background(), req)

	out := buf.String()
	if strings.Contains(out, testKey) {
		t.Errorf("logs leaked the API key:\n%s", out)
	}
	if strings.Contains(out, secretState) {
		t.Errorf("logs leaked the request state:\n%s", out)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
