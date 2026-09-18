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

	return &Client{
		apiKey:       cfg.apiKey,
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
	if len(req.Questions) == 0 {
		return nil, fmt.Errorf("%w: at least one question is required", ErrInvalidRequest)
	}
	if req.State == nil {
		return nil, fmt.Errorf("%w: state is required", ErrInvalidRequest)
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

// do performs one HTTP operation and returns the response body.
//
// Phase 1 makes exactly one attempt. Retries, backoff, and the overall budget
// arrive in Phase 4 and wrap this method rather than changing it.
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, string, error) {
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
