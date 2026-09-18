package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// DefaultTimeout bounds each HTTP operation, matching the official SDKs.
const DefaultTimeout = 10 * time.Second

// maxErrorBody caps how much of an error response we read into memory. A
// misconfigured proxy can return an unbounded HTML page, and an error path is
// no place to exhaust a heap.
const maxErrorBody = 1 << 20 // 1 MiB

// Client calls the TypeSafe System One API. It is safe for concurrent use and
// is meant to be created once and shared.
type Client struct {
	apiKey       string
	retry        RetryPolicy
	observer     func(AttemptInfo)
	clk          clock
	breaker      *CircuitBreaker
	budget       *Budget
	checkContext bool
	baseURL      string
	defaultModel string
	httpClient   *http.Client
	headers      http.Header
	logger       *slog.Logger
	userAgent    string
}

// NewClient builds a client.
//
// Configuration resolves in this order, each falling back to the next:
//
//	API key    WithAPIKey        -> TYPESAFE_API_KEY        -> error
//	Base URL   WithBaseURL       -> TYPESAFE_BASE_URL       -> https://api.typesafe.ai
//	Model      WithDefaultModel  -> TYPESAFE_DEFAULT_MODEL  -> jev-latest
//	Timeout    WithTimeout       -> 10s
//
// This mirrors the official Python and JavaScript SDKs, so a process already
// configured for either works here unchanged.
//
//	client, err := typesafe.NewClient()
//	if err != nil {
//	    return err
//	}
func NewClient(opts ...Option) (*Client, error) {
	cfg := &config{
		baseURL:      firstNonEmpty(os.Getenv(EnvBaseURL), DefaultBaseURL),
		defaultModel: firstNonEmpty(os.Getenv(EnvDefaultModel), DefaultModel),
		apiKey:       os.Getenv(EnvAPIKey),
		timeout:      DefaultTimeout,
		retry:        DefaultRetryPolicy(),
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}

	if strings.TrimSpace(cfg.apiKey) == "" {
		return nil, fmt.Errorf("%w: set %s or pass WithAPIKey", ErrNoAPIKey, EnvAPIKey)
	}
	if err := cfg.retry.validate(); err != nil {
		return nil, err
	}

	hc := cfg.httpClient
	switch {
	case hc == nil:
		hc = &http.Client{Timeout: cfg.timeout}
	case hc.Timeout == 0:
		// Never silently drop the timeout because a caller supplied a client.
		clone := *hc
		clone.Timeout = cfg.timeout
		hc = &clone
	}

	ua := fmt.Sprintf("typesafe-go/%s (go%s; %s/%s)",
		Version, strings.TrimPrefix(runtime.Version(), "go"), runtime.GOOS, runtime.GOARCH)
	if cfg.uaSuffix != "" {
		ua += " " + cfg.uaSuffix
	}

	clk := cfg.clk
	if clk == nil {
		clk = realClock{}
	}

	// The context-limit check is on unless explicitly disabled.
	checkContext := true
	if cfg.checkContextLimit != nil {
		checkContext = *cfg.checkContextLimit
	}

	return &Client{
		apiKey:       cfg.apiKey,
		retry:        cfg.retry,
		observer:     cfg.observer,
		clk:          clk,
		breaker:      cfg.breaker,
		budget:       cfg.budget,
		checkContext: checkContext,
		baseURL:      strings.TrimRight(cfg.baseURL, "/"),
		defaultModel: cfg.defaultModel,
		httpClient:   hc,
		headers:      cfg.headers.Clone(),
		logger:       cfg.logger,
		userAgent:    ua,
	}, nil
}

// DefaultModel reports the model used when a request leaves Model empty.
func (c *Client) DefaultModel() string { return c.defaultModel }

// BaseURL reports the API root this client targets.
func (c *Client) BaseURL() string { return c.baseURL }

// SystemOne evaluates one state against a map of named questions and returns
// one answer per question.
//
// Every question sees the same state and is evaluated independently and in
// parallel, so asking several costs little more than asking one. Pack every
// question about a given state into a single call.
//
// Returns a typed error for every documented failure: AuthenticationError,
// UnprocessableEntityError, RateLimitError, OverloadedError, and so on. Use
// errors.As to reach the detail, or errors.Is against the sentinels.
func (c *Client) SystemOne(ctx context.Context, req *SystemOneRequest) (*SystemOneResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: nil request", ErrInvalidConfig)
	}

	// Anything the server is known to reject fails here, before a round trip.
	// Warnings are advisory and are logged rather than returned, so that
	// SystemOne's signature stays the simple one; call req.Validate directly
	// to inspect them.
	warnings, err := req.Validate()
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		c.log(ctx, slog.LevelWarn, "typesafe question warning",
			"question", w.QuestionID, "warning", w.Message)
	}

	// Copy so that defaulting the model does not mutate the caller's value.
	wire := struct {
		State     any                 `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}{
		State:     req.State,
		Model:     firstNonEmpty(req.Model, c.defaultModel),
		Questions: req.Questions,
	}

	// Pre-flight, in cost order: the cheapest refusals come first, and none of
	// them touch the network.
	est := req.EstimateTokens()
	if c.checkContext {
		if err := est.Err(); err != nil {
			return nil, err
		}
	}
	if c.budget != nil {
		if err := c.budget.check(est.Total); err != nil {
			return nil, err
		}
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding request: %v", ErrInvalidRequest, err)
	}

	raw, requestID, err := c.do(ctx, http.MethodPost, SystemOnePath, body)
	if err != nil {
		return nil, err
	}

	var out SystemOneResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &ResponseValidationError{
			Endpoint:  endpoint(http.MethodPost, SystemOnePath),
			RequestID: requestID,
			Body:      raw,
			Err:       err,
		}
	}
	if out.Answers == nil {
		return nil, &ResponseValidationError{
			Endpoint:  endpoint(http.MethodPost, SystemOnePath),
			RequestID: requestID,
			Body:      raw,
			Err:       errors.New("response has no answers field"),
		}
	}
	for id := range req.Questions {
		if _, ok := out.Answers[id]; !ok {
			return nil, &ResponseValidationError{
				Endpoint:  endpoint(http.MethodPost, SystemOnePath),
				RequestID: requestID,
				Body:      raw,
				Err:       fmt.Errorf("question %q has no answer", id),
			}
		}
	}
	return &out, nil
}

// Models lists the model names and aliases this account may send in
// SystemOneRequest.Model.
//
// Versioned ids such as jev-1.13.0 are accepted by the API whether or not they
// appear here, so this is a discovery aid rather than an allow-list.
func (c *Client) Models(ctx context.Context) ([]ModelCard, error) {
	raw, requestID, err := c.do(ctx, http.MethodGet, ModelsPath, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Models []ModelCard `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &ResponseValidationError{
			Endpoint:  endpoint(http.MethodGet, ModelsPath),
			RequestID: requestID,
			Body:      raw,
			Err:       err,
		}
	}
	return out.Models, nil
}

// do performs the operation, retrying per the client's policy.
//
// The request body is marshalled once by the caller and handed here as bytes,
// so every attempt sends byte-identical content. Re-marshalling per attempt
// would risk drift — a map iterated in a different order is a different
// request, and a server that deduplicates would not recognize the retry.
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, string, error) {
	ep := endpoint(method, path)
	started := c.clk.Now()

	// The overall budget spans every attempt and every backoff, and is
	// distinct from the per-operation timeout on the HTTP client.
	//
	// It is tracked on the client's own clock rather than read back from
	// ctx.Deadline(). A context deadline is always real time, so comparing it
	// against an injected clock compares two unrelated timelines — which is
	// exactly the bug this comment replaced. The context timeout is still set,
	// because it is what actually interrupts an in-flight request.
	var budget time.Time
	if c.retry.Timeout > 0 {
		budget = started.Add(c.retry.Timeout)
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.retry.Timeout)
		defer cancel()
	}

	var (
		lastErr error
		made    int  // attempts actually performed
		retried bool // whether any backoff was actually waited out
	)
	for attempt := 0; ; attempt++ {
		// The breaker gates every attempt, not just the first: an outage that
		// begins mid-retry should stop the remaining attempts too.
		if err := c.guardOpen(ctx); err != nil {
			if lastErr != nil {
				// Report what was actually failing, with the breaker's
				// rejection as context rather than as a replacement.
				return nil, "", errors.Join(err, lastErr)
			}
			return nil, "", err
		}

		made++
		raw, requestID, err := c.attempt(ctx, method, path, body)
		c.guardRecord(err)
		if err == nil {
			return raw, requestID, nil
		}
		lastErr = err

		if attempt >= c.retry.MaxRetries || !c.retry.shouldRetry(err) {
			break
		}

		delay, honored := c.retry.retryAfterFrom(err)
		if honored {
			// A server asking for longer than we are willing to wait is not a
			// reason to block the caller; it is a reason to return the error
			// they can act on. The retry-after is already on the typed error.
			if c.retry.MaxRetryAfter > 0 && delay > c.retry.MaxRetryAfter {
				c.log(ctx, slog.LevelWarn, "typesafe retry-after exceeds the cap",
					"endpoint", ep, "retry_after", delay, "cap", c.retry.MaxRetryAfter)
				break
			}
		} else {
			delay = c.retry.delayFor(attempt)
		}

		// Never sleep past the budget: waiting out a deadline we already know
		// we will miss wastes the caller's time for no possible benefit.
		if !budget.IsZero() {
			if remaining := budget.Sub(c.clk.Now()); delay >= remaining {
				c.log(ctx, slog.LevelWarn, "typesafe backoff would exceed the retry budget",
					"endpoint", ep, "delay_ms", delay.Milliseconds(),
					"remaining_ms", remaining.Milliseconds())
				break
			}
		}

		status := 0
		var api *APIError
		if errors.As(err, &api) {
			status = api.Status
		}
		info := AttemptInfo{
			Attempt:           attempt + 1,
			Err:               err,
			Status:            status,
			Delay:             delay,
			RetryAfterHonored: honored,
			Elapsed:           c.clk.Now().Sub(started),
		}
		c.log(ctx, slog.LevelWarn, "typesafe retrying",
			"endpoint", ep, "attempt", info.Attempt, "status", status,
			"delay_ms", delay.Milliseconds(), "retry_after_honored", honored)
		if c.observer != nil {
			c.observer(info)
		}

		if err := c.clk.Sleep(ctx, delay); err != nil {
			// Cancelled or out of budget mid-backoff. Report the API failure
			// that caused the wait, not the timer: the caller wants to know
			// why we were waiting, not that a timer was interrupted.
			return nil, "", c.exhausted(made, started, lastErr)
		}
		retried = true
	}

	// Only claim exhaustion when retrying actually happened. Breaking out
	// early — a non-retryable status, a retry-after past the cap, a backoff
	// that would outlast the budget — is not the same thing, and reporting
	// "gave up after 3 attempts" when one was made would be a lie in an error
	// message someone is trying to debug from.
	if retried {
		return nil, "", c.exhausted(made, started, lastErr)
	}
	return nil, "", lastErr
}

// exhausted wraps the terminal error, keeping it reachable through errors.As.
func (c *Client) exhausted(attempts int, started time.Time, last error) error {
	return &retriesExhaustedError{
		attempts: attempts,
		elapsed:  c.clk.Now().Sub(started),
		last:     last,
	}
}

// attempt performs exactly one HTTP operation.
func (c *Client) attempt(ctx context.Context, method, path string, body []byte) ([]byte, string, error) {
	ep := endpoint(method, path)

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, "", fmt.Errorf("%w: building request: %v", ErrInvalidConfig, err)
	}

	for k, vs := range c.headers {
		for _, v := range vs {
			httpReq.Header.Add(k, v)
		}
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
		// Set explicitly so a retry (Phase 4) re-sends identical bytes.
		httpReq.ContentLength = int64(len(body))
	}

	started := time.Now()
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", c.transportError(ctx, ep, err)
	}
	defer func() {
		// Drain a little so the connection can be reused, then close.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	requestID := resp.Header.Get(RequestIDHeader)
	c.log(ctx, slog.LevelDebug, "typesafe request",
		"endpoint", ep,
		"status", resp.StatusCode,
		"request_id", requestID,
		"duration_ms", time.Since(started).Milliseconds(),
	)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		apiErr := newAPIError(ep, resp.StatusCode, resp.Header, errBody, []byte(c.apiKey))
		c.log(ctx, slog.LevelWarn, "typesafe request failed",
			"endpoint", ep, "status", resp.StatusCode, "request_id", requestID)
		return nil, requestID, apiErr
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		// A truncated body is a transport failure, not a malformed response:
		// we never saw the whole thing, so we cannot judge its shape.
		return nil, requestID, c.transportError(ctx, ep, err)
	}
	return raw, requestID, nil
}

// transportError classifies a failure that produced no usable response.
//
// A caller's own cancellation stays context.Canceled rather than becoming a
// TimeoutError: shutting down is not a fault, and reporting it as one makes
// graceful-exit paths log noise.
func (c *Client) transportError(ctx context.Context, ep string, err error) error {
	switch {
	case errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled:
		return fmt.Errorf("typesafe: %s: %w", ep, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return &TimeoutError{Endpoint: ep, Err: err}
	default:
		return &ConnectionError{Endpoint: ep, Err: err}
	}
}

func (c *Client) log(ctx context.Context, level slog.Level, msg string, args ...any) {
	if c.logger == nil {
		return
	}
	c.logger.Log(ctx, level, msg, args...)
}

func endpoint(method, path string) string { return method + " " + path }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
