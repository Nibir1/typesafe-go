package typesafe

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"
)

// Handler performs one logical SystemOne call.
//
// Interceptors wrap a Handler to observe or alter a call. The signature
// deliberately matches Client.SystemOne, so the client itself is a Handler and
// an interceptor chain composes onto it without adaptation.
type Handler func(ctx context.Context, req *SystemOneRequest) (*SystemOneResponse, error)

// Interceptor wraps a Handler, in the style of a gRPC unary interceptor.
//
//	func timing(next typesafe.Handler) typesafe.Handler {
//	    return func(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
//	        start := time.Now()
//	        resp, err := next(ctx, req)
//	        metrics.Observe(time.Since(start), err)
//	        return resp, err
//	    }
//	}
//
// # What an interceptor sees
//
// One *logical* call. Retries, backoff and the circuit breaker all happen
// beneath the innermost handler, so an interceptor observes a single attempt
// from the caller's point of view no matter how many HTTP requests it took.
// That is almost always what instrumentation wants: a latency histogram should
// record what the caller waited for, not one bar per retry. When you do want
// per-attempt visibility, use WithRetryObserver, which is the layer below.
type Interceptor func(next Handler) Handler

// WithInterceptor adds interceptors to a client.
//
// They run outermost-first, in the order given, so the first interceptor
// listed is the first to see a request and the last to see its response — the
// same nesting as gRPC and as net/http middleware.
//
//	client, err := typesafe.NewClient(
//	    typesafe.WithInterceptor(tracing, metrics, logging),
//	)
//	// tracing -> metrics -> logging -> the API
//
// # Why this is an option and not a Use method
//
// The roadmap sketched `client.Use(...)`. A method mutating a live client is a
// data race waiting to happen: Client is documented as safe for concurrent use
// and is meant to be built once and shared, so a Use call from one goroutine
// while another is mid-request would be exactly the bug this SDK should not
// ship. Composing at construction makes the chain immutable, which costs
// nothing — the set of interceptors is a deployment decision, not a per-call
// one.
func WithInterceptor(interceptors ...Interceptor) Option {
	return func(c *config) error {
		for i, in := range interceptors {
			if in == nil {
				return fmt.Errorf("%w: interceptor %d is nil", ErrInvalidConfig, i)
			}
		}
		c.interceptors = append(c.interceptors, interceptors...)
		return nil
	}
}

// chain composes interceptors around a base handler.
//
// Built once at construction rather than per call: the composition is pure
// function wrapping, and redoing it on every request would allocate for no
// reason.
func chain(base Handler, interceptors []Interceptor) Handler {
	// Wrap in reverse so the first interceptor ends up outermost.
	for i := len(interceptors) - 1; i >= 0; i-- {
		base = interceptors[i](base)
	}
	return base
}

// PanicError is a panic recovered from an interceptor or a hook.
//
// A panic in instrumentation should not take down a request path. Recovering
// it and returning it as an error keeps the caller's error handling in charge,
// and the captured stack points at the interceptor rather than at the recovery
// site — which is the difference between a two-minute fix and an afternoon.
type PanicError struct {
	// Value is whatever was passed to panic.
	Value any

	// Stack is the stack trace captured at the moment of recovery.
	Stack []byte
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("typesafe: panic in an interceptor or hook: %v", e.Value)
}

// Unwrap returns the panic value when it was itself an error.
func (e *PanicError) Unwrap() error {
	if err, ok := e.Value.(error); ok {
		return err
	}
	return nil
}

// recoverPanic wraps a handler so a panic inside it becomes an error.
func recoverPanic(next Handler) Handler {
	return func(ctx context.Context, req *SystemOneRequest) (resp *SystemOneResponse, err error) {
		defer func() {
			if r := recover(); r != nil {
				// Capture here, where the panicking frames are still on the
				// stack. Doing it any later loses the only useful information.
				err = &PanicError{Value: r, Stack: debug.Stack()}
				resp = nil
			}
		}()
		return next(ctx, req)
	}
}

// Hooks are callbacks at fixed points in a call, for code that wants to
// observe without composing an interceptor.
//
// Every hook is optional. They run synchronously on the calling goroutine, so
// keep them quick; a slow hook is latency the caller pays. A panic in one is
// recovered and returned as a *PanicError.
//
//	typesafe.WithHooks(typesafe.Hooks{
//	    OnResponse: func(ctx context.Context, i typesafe.CallInfo) {
//	        log.Printf("%s took %s, %d tokens", i.RequestID, i.Duration, i.InputTokens)
//	    },
//	})
type Hooks struct {
	// OnRequest runs before a request is sent.
	OnRequest func(ctx context.Context, req *SystemOneRequest)

	// OnResponse runs after a successful call.
	OnResponse func(ctx context.Context, info CallInfo)

	// OnError runs after a failed call.
	OnError func(ctx context.Context, info CallInfo)
}

// CallInfo describes a completed call.
type CallInfo struct {
	// RequestID is the client-generated correlation id, when one is
	// configured. See WithRequestID.
	RequestID string

	// Duration is how long the whole call took, retries included.
	Duration time.Duration

	// Questions is how many questions were asked.
	Questions int

	// Model is the versioned model that answered, empty on failure.
	Model string

	// InputTokens and OutputTokens come from the response, zero on failure.
	InputTokens  int
	OutputTokens int

	// Err is the failure, nil on success.
	Err error
}

// WithHooks installs callbacks at fixed points in a call.
//
// Hooks are implemented as an interceptor, so they nest with any other
// interceptors in the order everything was declared. Use Hooks for the common
// case of observing a call; use WithInterceptor when you need to alter one.
func WithHooks(h Hooks) Option {
	return WithInterceptor(hookInterceptor(h))
}

func hookInterceptor(h Hooks) Interceptor {
	return func(next Handler) Handler {
		return func(ctx context.Context, req *SystemOneRequest) (*SystemOneResponse, error) {
			if h.OnRequest != nil {
				h.OnRequest(ctx, req)
			}

			start := time.Now()
			resp, err := next(ctx, req)

			info := CallInfo{
				RequestID: RequestIDFrom(ctx),
				Duration:  time.Since(start),
				Err:       err,
			}
			if req != nil {
				info.Questions = len(req.Questions)
			}
			if resp != nil {
				info.Model = resp.Model
				info.InputTokens = resp.Usage.InputTokens
				info.OutputTokens = resp.Usage.OutputTokens
			}

			if err != nil {
				if h.OnError != nil {
					h.OnError(ctx, info)
				}
			} else if h.OnResponse != nil {
				h.OnResponse(ctx, info)
			}
			return resp, err
		}
	}
}

// requestIDKey carries a per-call correlation id on the context.
type requestIDKey struct{}

// RequestIDFrom returns the correlation id this SDK generated for the call, or
// "" when none was configured.
//
// Useful inside a hook or an interceptor to tie SDK activity to the rest of a
// trace. Distinct from APIError.RequestID, which is the id *TypeSafe* assigned
// and is the one to quote in a support ticket.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// WithRequestID generates a correlation id for every call.
//
// The id is placed on the context, where hooks and interceptors can read it
// with RequestIDFrom, and is logged alongside every message about the call.
//
// It is **not** sent to the API by default. TypeSafe documents no request
// header for a client-supplied id, and inventing one risks colliding with a
// meaning the server later assigns. Pass a header name to send it anyway:
//
//	typesafe.WithRequestID(uuid.NewString)                    // local only
//	typesafe.WithRequestID(uuid.NewString, "x-correlation-id") // also sent
func WithRequestID(fn func() string, header ...string) Option {
	return func(c *config) error {
		if fn == nil {
			return fmt.Errorf("%w: WithRequestID given nil", ErrInvalidConfig)
		}
		if len(header) > 1 {
			return fmt.Errorf("%w: WithRequestID takes at most one header name", ErrInvalidConfig)
		}
		c.requestID = fn
		if len(header) == 1 {
			if header[0] == "" {
				return fmt.Errorf("%w: WithRequestID given an empty header name", ErrInvalidConfig)
			}
			c.requestIDHeader = header[0]
		}
		return nil
	}
}

// WithLogging returns an interceptor that logs each call through slog.
//
// A ready-made alternative to WithLogger for callers who want request-level
// logging composed with their other interceptors rather than emitted from
// inside the client.
//
// It logs the request id, question count, duration, model and token usage. It
// does **not** log the state, the questions, or anything derived from them:
// state is the caller's data and routinely contains personal information, and
// a logging helper that leaks it by default is worse than none.
func WithLogging(l *slog.Logger) Interceptor {
	return func(next Handler) Handler {
		return func(ctx context.Context, req *SystemOneRequest) (*SystemOneResponse, error) {
			start := time.Now()
			resp, err := next(ctx, req)

			attrs := []any{
				"duration_ms", time.Since(start).Milliseconds(),
			}
			if req != nil {
				attrs = append(attrs, "questions", len(req.Questions))
			}
			if id := RequestIDFrom(ctx); id != "" {
				attrs = append(attrs, "request_id", id)
			}
			if resp != nil {
				attrs = append(attrs,
					"model", resp.Model,
					"input_tokens", resp.Usage.InputTokens,
					"output_tokens", resp.Usage.OutputTokens)
			}
			if err != nil {
				var api *APIError
				if errorsAs(err, &api) {
					attrs = append(attrs, "status", api.Status, "api_request_id", api.RequestID)
				}
				// err.Error() is scrubbed of credentials by the error types.
				attrs = append(attrs, "error", err.Error())
				l.LogAttrs(ctx, slog.LevelWarn, "typesafe call failed", toAttrs(attrs)...)
				return resp, err
			}
			l.LogAttrs(ctx, slog.LevelInfo, "typesafe call", toAttrs(attrs)...)
			return resp, nil
		}
	}
}

func toAttrs(kv []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		out = append(out, slog.Any(key, kv[i+1]))
	}
	return out
}
