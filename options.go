package typesafe

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Option configures a Client. Options are applied in order, so a later one
// overrides an earlier one.
type Option func(*config) error

type config struct {
	apiKey            string
	baseURL           string
	defaultModel      string
	timeout           time.Duration
	httpClient        *http.Client
	headers           http.Header
	logger            *slog.Logger
	uaSuffix          string
	retry             RetryPolicy
	observer          func(context.Context, AttemptInfo)
	clk               clock
	breaker           *CircuitBreaker
	budget            *Budget
	checkContextLimit *bool
	interceptors      []Interceptor
	requestID         func() string
	requestIDHeader   string
}

// WithAPIKey sets the key explicitly, taking precedence over TYPESAFE_API_KEY.
//
// Prefer the environment variable in deployed code: a key in a source file is
// a key in your git history.
func WithAPIKey(key string) Option {
	return func(c *config) error {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: WithAPIKey given an empty key", ErrInvalidConfig)
		}
		c.apiKey = key
		return nil
	}
}

// WithBaseURL overrides the API root, for a proxy, a gateway, or a test
// server. Takes precedence over TYPESAFE_BASE_URL.
func WithBaseURL(raw string) Option {
	return func(c *config) error {
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("%w: base URL %q: %v", ErrInvalidConfig, raw, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("%w: base URL %q must be http or https", ErrInvalidConfig, raw)
		}
		if u.Host == "" {
			return fmt.Errorf("%w: base URL %q has no host", ErrInvalidConfig, raw)
		}
		c.baseURL = strings.TrimRight(raw, "/")
		return nil
	}
}

// WithDefaultModel sets the model used when a request leaves Model empty.
// Takes precedence over TYPESAFE_DEFAULT_MODEL.
func WithDefaultModel(model string) Option {
	return func(c *config) error {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("%w: WithDefaultModel given an empty model", ErrInvalidConfig)
		}
		c.defaultModel = model
		return nil
	}
}

// WithTimeout bounds each HTTP operation. The default is 10s, matching the
// official SDKs.
//
// This is per operation, not per call: a single SystemOne may span several
// operations when retrying, bounded separately by RetryPolicy.Timeout.
// Whichever fires first wins.
func WithTimeout(d time.Duration) Option {
	return func(c *config) error {
		if d <= 0 {
			return fmt.Errorf("%w: timeout must be positive, got %s", ErrInvalidConfig, d)
		}
		c.timeout = d
		return nil
	}
}

// WithHTTPClient supplies your own *http.Client — for a custom transport,
// connection pool, proxy, or instrumentation.
//
// The client's Timeout is respected as given. If it is zero, WithTimeout (or
// the 10s default) is applied to a shallow copy, so that supplying a client
// never silently removes the timeout.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) error {
		if hc == nil {
			return fmt.Errorf("%w: WithHTTPClient given nil", ErrInvalidConfig)
		}
		c.httpClient = hc
		return nil
	}
}

// WithHeader sets a header sent on every request.
//
// Authorization cannot be set this way — it is derived from the API key, and
// allowing an override here would make credential handling ambiguous.
func WithHeader(key, value string) Option {
	return func(c *config) error {
		if http.CanonicalHeaderKey(key) == "Authorization" {
			return fmt.Errorf("%w: set the credential with WithAPIKey, not WithHeader", ErrInvalidConfig)
		}
		if c.headers == nil {
			c.headers = make(http.Header)
		}
		c.headers.Set(key, value)
		return nil
	}
}

// WithLogger attaches a structured logger.
//
// The SDK never logs the API key, the Authorization header, or the request
// state at any level: state is the caller's data and frequently contains
// personal information. Request ids, statuses, and timings are logged.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) error {
		if l == nil {
			return fmt.Errorf("%w: WithLogger given nil", ErrInvalidConfig)
		}
		c.logger = l
		return nil
	}
}

// WithUserAgentSuffix appends an identifier to the SDK's User-Agent, for
// applications that want their own name in TypeSafe's logs. The SDK's own
// identity is always sent first and cannot be replaced.
func WithUserAgentSuffix(suffix string) Option {
	return func(c *config) error {
		s := strings.TrimSpace(suffix)
		if s == "" {
			return fmt.Errorf("%w: WithUserAgentSuffix given an empty suffix", ErrInvalidConfig)
		}
		if strings.ContainsAny(s, "\r\n") {
			return fmt.Errorf("%w: user agent suffix must not contain newlines", ErrInvalidConfig)
		}
		c.uaSuffix = s
		return nil
	}
}

// WithRetryPolicy replaces the whole retry policy.
//
// The default matches the official Python and JavaScript SDKs: two retries,
// 500ms initial backoff doubling to a 5s ceiling, ±25% jitter, retrying 408,
// 429 and 5xx, honoring retry-after, within a 30s overall budget.
//
//	typesafe.WithRetryPolicy(typesafe.NoRetry())
//
//	p := typesafe.DefaultRetryPolicy()
//	p.MaxRetries = 5
//	typesafe.WithRetryPolicy(p)
func WithRetryPolicy(p RetryPolicy) Option {
	return func(c *config) error {
		if err := p.validate(); err != nil {
			return err
		}
		c.retry = p
		return nil
	}
}

// WithMaxRetries adjusts only the retry count, leaving the rest of the default
// policy alone. Zero disables retrying.
func WithMaxRetries(n int) Option {
	return func(c *config) error {
		if n < 0 {
			return fmt.Errorf("%w: max retries must not be negative, got %d", ErrInvalidConfig, n)
		}
		c.retry.MaxRetries = n
		return nil
	}
}

// WithRetryObserver registers a callback invoked after each failed attempt
// that will be retried, before the backoff begins.
//
// Useful for metrics and for surfacing retry behavior in traces. It is called
// synchronously on the calling goroutine, so keep it quick and do not block.
//
//	typesafe.WithRetryObserver(func(ctx context.Context, a typesafe.AttemptInfo) {
//	    metrics.Retries.WithLabelValues(strconv.Itoa(a.Status)).Inc()
//	})
//
// The context is the one the call was made with. Tracing integrations need it:
// without it an attempt cannot be attached to the span of the call it belongs
// to, and a per-attempt span would be an orphan.
func WithRetryObserver(fn func(context.Context, AttemptInfo)) Option {
	return func(c *config) error {
		if fn == nil {
			return fmt.Errorf("%w: WithRetryObserver given nil", ErrInvalidConfig)
		}
		c.observer = fn
		return nil
	}
}

// withClock injects a clock, for tests that need backoff without real waiting.
// Unexported: production callers have no reason to replace time.
func withClock(k clock) Option {
	return func(c *config) error {
		c.clk = k
		return nil
	}
}
