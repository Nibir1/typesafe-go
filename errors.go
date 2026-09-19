package typesafe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors, for callers who prefer errors.Is over errors.As. Every
// typed error below matches exactly one of these.
//
//	if errors.Is(err, typesafe.ErrRateLimit) { ... }
//
// The typed forms carry more: status, request id, retry-after, and for a 422
// the exact field the server rejected. Prefer errors.As when you need those.
var (
	// ErrNoAPIKey means no key was supplied and TYPESAFE_API_KEY is unset.
	ErrNoAPIKey = errors.New("typesafe: no API key")

	ErrBadRequest       = errors.New("typesafe: bad request")
	ErrAuthentication   = errors.New("typesafe: authentication failed")
	ErrPermissionDenied = errors.New("typesafe: permission denied")
	ErrNotFound         = errors.New("typesafe: not found")
	ErrInvalidRequest   = errors.New("typesafe: request failed validation")
	ErrRateLimit        = errors.New("typesafe: rate limited")
	ErrOverloaded       = errors.New("typesafe: service overloaded")
	ErrInternalServer   = errors.New("typesafe: server error")
	ErrConnection       = errors.New("typesafe: connection failed")
	ErrTimeout          = errors.New("typesafe: request timed out")
	ErrInvalidResponse  = errors.New("typesafe: malformed response")
	ErrInvalidConfig    = errors.New("typesafe: invalid client configuration")
)

// APIError is any non-2xx response. Every status-specific error below wraps
// one, so errors.As(err, &apiErr) succeeds for all of them.
type APIError struct {
	// Status is the HTTP status code.
	Status int

	// Body is the raw response body, for statuses whose shape we do not model.
	Body []byte

	// Header is the response header. Never contains request credentials.
	Header http.Header

	// Endpoint is the method and path, without credentials or query.
	Endpoint string

	// RequestID is the x-typesafe-request-id header, or "" if absent. Worth
	// quoting in a support ticket.
	RequestID string

	// Detail is the parsed field-level validation failures from a 422 body.
	// Empty for every other status. See ValidationDetail.
	Detail []ValidationDetail

	// Reason is the server's typed error message, used by 400 and 401.
	//
	// The API returns "detail" in two different shapes: an array of
	// ValidationDetail for schema failures (422), and a single object with
	// error_type and message for everything else. Neither shape is published;
	// both were observed against the live API. A client that assumes one shape
	// silently loses the other, which is why both are modeled here.
	Reason *ErrorDetail

	// secret is the credential in play when this error was produced, so that
	// Error() can scrub it. A server that echoes the key back in its error
	// body would otherwise leak it into every log line that prints the error.
	//
	// Body keeps the server's bytes verbatim: it is the caller's data and
	// theirs to handle. Only the rendered message is scrubbed.
	secret string
}

// scrub removes the credential from text destined for a log or a message.
func (e *APIError) scrub(s string) string {
	if e.secret == "" {
		return s
	}
	return strings.ReplaceAll(s, e.secret, "[REDACTED]")
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "typesafe: %s: %d %s", e.Endpoint, e.Status, http.StatusText(e.Status))
	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request %s)", e.RequestID)
	}
	switch {
	case e.Reason != nil && e.Reason.Message != "":
		fmt.Fprintf(&b, ": %s", e.scrub(e.Reason.String()))
	case len(e.Detail) > 0:
		fmt.Fprintf(&b, ": %s", e.scrub(e.Detail[0].String()))
		if n := len(e.Detail) - 1; n > 0 {
			fmt.Fprintf(&b, " (and %d more)", n)
		}
	case len(e.Body) > 0:
		fmt.Fprintf(&b, ": %s", e.scrub(truncate(string(e.Body), 200)))
	}
	return b.String()
}

// ValidationDetail is one entry from a 422 response body. The server reports
// a path to the offending field, so a caller can see precisely which question
// and which field were rejected rather than re-reading their whole request.
type ValidationDetail struct {
	// Loc is the path to the offending field, e.g.
	// ["body", "questions", "frustration", "criteria"]. Elements are strings
	// or integers.
	Loc []any `json:"loc"`

	// Msg is the server's human-readable explanation.
	Msg string `json:"msg"`

	// Type is the machine-readable error kind, e.g. "missing".
	Type string `json:"type"`

	// Input is the value that failed validation, when the server includes it.
	Input any `json:"input,omitempty"`

	// Ctx carries kind-specific context, when the server includes it.
	Ctx map[string]any `json:"ctx,omitempty"`
}

// ErrorDetail is the object form of the "detail" field, returned for errors
// that are not per-field schema violations.
//
//	{"detail": {"error_type": "api_usage_error", "message": "Unknown model: x"}}
//
// Observed error_type values include "authentication_error" and
// "api_usage_error". The set is not published, so treat it as open.
type ErrorDetail struct {
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

func (d ErrorDetail) String() string {
	if d.ErrorType != "" {
		return d.ErrorType + ": " + d.Message
	}
	return d.Message
}

// Path renders Loc as a dotted path with bracketed indices, e.g.
// "body.questions.frustration.criteria" or "body.questions.q.criteria[0]".
func (d ValidationDetail) Path() string {
	var b strings.Builder
	for _, part := range d.Loc {
		switch v := part.(type) {
		case string:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(v)
		case json.Number:
			fmt.Fprintf(&b, "[%s]", v)
		case float64: // encoding/json's default number type
			fmt.Fprintf(&b, "[%d]", int64(v))
		case int:
			fmt.Fprintf(&b, "[%d]", v)
		default:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			fmt.Fprintf(&b, "%v", v)
		}
	}
	return b.String()
}

func (d ValidationDetail) String() string {
	if p := d.Path(); p != "" {
		return p + ": " + d.Msg
	}
	return d.Msg
}

// Status-specific errors. Each embeds *APIError, so the full response detail
// is available on any of them.

// BadRequestError is a 400: the request was well-formed against the schema but
// the server rejected its content.
//
// Undocumented in both the OpenAPI spec (which declares only 200 and 422) and
// the prose docs. Observed for an unknown model name and for a Score with more
// than ten levels. Not retryable.
type BadRequestError struct{ *APIError }

func (e *BadRequestError) Unwrap() error        { return e.APIError }
func (e *BadRequestError) Is(target error) bool { return target == ErrBadRequest }

// AuthenticationError is a 401: the key is missing or invalid.
type AuthenticationError struct{ *APIError }

func (e *AuthenticationError) Unwrap() error { return e.APIError }
func (e *AuthenticationError) Is(target error) bool {
	return target == ErrAuthentication
}

// PermissionDeniedError is a 403.
type PermissionDeniedError struct{ *APIError }

func (e *PermissionDeniedError) Unwrap() error        { return e.APIError }
func (e *PermissionDeniedError) Is(target error) bool { return target == ErrPermissionDenied }

// NotFoundError is a 404.
type NotFoundError struct{ *APIError }

func (e *NotFoundError) Unwrap() error        { return e.APIError }
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// UnprocessableEntityError is a 422: the request body failed validation.
//
// This is never retried — the same bytes will fail the same way. Read Detail
// to find out which field the server rejected.
type UnprocessableEntityError struct{ *APIError }

func (e *UnprocessableEntityError) Unwrap() error        { return e.APIError }
func (e *UnprocessableEntityError) Is(target error) bool { return target == ErrInvalidRequest }

// RateLimitError is a 429.
type RateLimitError struct {
	*APIError

	// RetryAfter is the delay the server asked for, or 0 if it did not send a
	// retry-after header. Zero does not mean "retry immediately" — it means
	// the server expressed no preference, so use your own backoff.
	RetryAfter time.Duration
}

func (e *RateLimitError) Unwrap() error        { return e.APIError }
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimit }

// OverloadedError is a 529: TypeSafe is temporarily overloaded. Distinct from
// InternalServerError because it is explicitly transient and expected under
// load, not a defect.
type OverloadedError struct {
	*APIError
	RetryAfter time.Duration
}

func (e *OverloadedError) Unwrap() error        { return e.APIError }
func (e *OverloadedError) Is(target error) bool { return target == ErrOverloaded }

// InternalServerError is a 5xx other than 529.
type InternalServerError struct{ *APIError }

func (e *InternalServerError) Unwrap() error        { return e.APIError }
func (e *InternalServerError) Is(target error) bool { return target == ErrInternalServer }

// ConnectionError is a transport failure: the request never produced a
// response. Safe to retry.
type ConnectionError struct {
	Endpoint string
	Err      error
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("typesafe: %s: connection failed: %v", e.Endpoint, e.Err)
}
func (e *ConnectionError) Unwrap() error        { return e.Err }
func (e *ConnectionError) Is(target error) bool { return target == ErrConnection }

// TimeoutError is a deadline exceeded while waiting for a response.
//
// A caller's own canceled context surfaces as context.Canceled, not as this:
// deliberate cancellation is not a timeout, and conflating them makes shutdown
// paths log spurious failures.
type TimeoutError struct {
	Endpoint string
	Err      error
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("typesafe: %s: timed out: %v", e.Endpoint, e.Err)
}
func (e *TimeoutError) Unwrap() error        { return e.Err }
func (e *TimeoutError) Is(target error) bool { return target == ErrTimeout }

// ResponseValidationError is a 2xx whose body does not match the contract.
// Getting one means either the API changed or this SDK's reading of it is
// wrong; both are worth reporting.
type ResponseValidationError struct {
	Endpoint  string
	RequestID string
	Body      []byte
	Err       error
}

func (e *ResponseValidationError) Error() string {
	s := fmt.Sprintf("typesafe: %s: malformed response: %v", e.Endpoint, e.Err)
	if e.RequestID != "" {
		s += " (request " + e.RequestID + ")"
	}
	return s
}
func (e *ResponseValidationError) Unwrap() error        { return e.Err }
func (e *ResponseValidationError) Is(target error) bool { return target == ErrInvalidResponse }

// newAPIError builds the right typed error for a status code.
func newAPIError(endpoint string, status int, header http.Header, body, secret []byte) error {
	base := &APIError{
		Status:    status,
		Body:      body,
		Header:    header,
		Endpoint:  endpoint,
		RequestID: header.Get(RequestIDHeader),
		secret:    string(secret),
	}
	base.Detail, base.Reason = parseErrorBody(body)

	switch {
	case status == http.StatusBadRequest:
		return &BadRequestError{base}
	case status == http.StatusUnauthorized:
		return &AuthenticationError{base}
	case status == http.StatusForbidden:
		return &PermissionDeniedError{base}
	case status == http.StatusNotFound:
		return &NotFoundError{base}
	case status == http.StatusUnprocessableEntity:
		return &UnprocessableEntityError{base}
	case status == http.StatusTooManyRequests:
		return &RateLimitError{APIError: base, RetryAfter: parseRetryAfter(header, timeNow())}
	case status == StatusOverloaded:
		return &OverloadedError{APIError: base, RetryAfter: parseRetryAfter(header, timeNow())}
	case status >= 500:
		return &InternalServerError{base}
	default:
		return base
	}
}

// StatusOverloaded is 529, which net/http does not name. TypeSafe returns it
// when the service is temporarily saturated.
const StatusOverloaded = 529

// timeNow is a variable so tests can pin the clock for HTTP-date parsing.
var timeNow = time.Now

// parseErrorBody extracts whichever "detail" shape the server used. A body
// matching neither is not an error — the caller still has APIError.Body.
func parseErrorBody(body []byte) ([]ValidationDetail, *ErrorDetail) {
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil || len(envelope.Detail) == 0 {
		return nil, nil
	}

	switch firstJSONToken(envelope.Detail) {
	case '[':
		var out []ValidationDetail
		d := json.NewDecoder(strings.NewReader(string(envelope.Detail)))
		d.UseNumber()
		if err := d.Decode(&out); err != nil {
			return nil, nil
		}
		return out, nil
	case '{':
		var out ErrorDetail
		if err := json.Unmarshal(envelope.Detail, &out); err != nil {
			return nil, nil
		}
		return nil, &out
	case '"':
		// A bare string. Not observed, but cheap to tolerate.
		var msg string
		if err := json.Unmarshal(envelope.Detail, &msg); err != nil {
			return nil, nil
		}
		return nil, &ErrorDetail{Message: msg}
	}
	return nil, nil
}

func firstJSONToken(b []byte) byte {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return c
		}
	}
	return 0
}

// parseRetryAfter reads a retry-after header in either documented form:
// delay-seconds, or an HTTP-date. Returns 0 when absent or unparseable, and
// never returns a negative duration.
func parseRetryAfter(header http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
