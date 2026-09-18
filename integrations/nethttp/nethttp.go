// Package nethttp puts a TypeSafe client in the request context.
//
//	client, _ := typesafe.NewClient()
//	mux := http.NewServeMux()
//	mux.HandleFunc("/triage", triage)
//	http.ListenAndServe(":8080", nethttp.Middleware(client)(mux))
//
//	func triage(w http.ResponseWriter, r *http.Request) {
//	    client := nethttp.MustFrom(r.Context())
//	    resp, err := client.SystemOne(r.Context(), req)
//	}
//
// # Why this is worth a package
//
// Injecting a client into a context is four lines, and on its own it would not
// be. What makes it worth having is the second half: correlating the inbound
// request with the calls it causes.
//
// A production incident with an LLM API almost always starts from one bad
// response, and the first question is which inbound request produced it. That
// needs the caller's correlation id — an X-Request-Id from a load balancer, a
// trace id, whatever the deployment uses — to reach the SDK's own request id
// header, so the two appear together in the API's logs and in yours.
// PropagateRequestID does that, and doing it by hand means remembering it at
// every call site.
package nethttp

import (
	"context"
	"net/http"

	typesafe "github.com/nibir1/typesafe-go"
)

// DefaultCorrelationHeaders are the inbound headers checked, in order, for a
// correlation id.
//
// Ordered by specificity: a value a proxy set deliberately for this request
// beats the W3C traceparent, which identifies the whole distributed trace
// rather than this hop.
var DefaultCorrelationHeaders = []string{
	"X-Request-Id",
	"X-Correlation-Id",
	"traceparent",
}

type clientKey struct{}

// Middleware returns middleware that puts client in every request's context.
//
// The client is shared, not copied: *typesafe.Client is safe for concurrent
// use and is meant to be built once for the process. Building one per request
// would discard the connection pool, the circuit breaker's state and the
// budget's accounting, all of which only mean anything across requests.
func Middleware(client *typesafe.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), client)))
		})
	}
}

// NewContext returns ctx carrying client.
func NewContext(ctx context.Context, client *typesafe.Client) context.Context {
	return context.WithValue(ctx, clientKey{}, client)
}

// From returns the client in ctx, if any.
func From(ctx context.Context) (*typesafe.Client, bool) {
	c, ok := ctx.Value(clientKey{}).(*typesafe.Client)
	return c, ok
}

// MustFrom returns the client in ctx, panicking if there is none.
//
// For handlers that are only ever mounted behind Middleware, where a missing
// client is a wiring mistake rather than a runtime condition — and one that
// would otherwise surface as a nil dereference several frames away. Use From
// where the handler can run either way.
func MustFrom(ctx context.Context) *typesafe.Client {
	c, ok := From(ctx)
	if !ok {
		panic("typesafe/nethttp: no client in context; is the handler behind Middleware?")
	}
	return c
}

// CorrelationMiddleware reuses the inbound request's correlation id as the
// SDK's request id for every call made while serving that request.
//
//	handler = nethttp.CorrelationMiddleware()(nethttp.Middleware(client)(mux))
//
// One id then ties the inbound request, this SDK's logs, and the API's own
// records together. Without it every call invents its own id and correlating
// them means joining on timestamps — which is exactly the work nobody wants to
// be doing during an incident.
//
// Headers are checked in order; DefaultCorrelationHeaders is used when none
// are given. A request with no correlation header is left alone, so the
// client's own WithRequestID generator still applies rather than the id being
// blanked out.
func CorrelationMiddleware(headers ...string) func(http.Handler) http.Handler {
	if len(headers) == 0 {
		headers = DefaultCorrelationHeaders
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := firstHeader(r, headers)
			if id == "" {
				next.ServeHTTP(w, r)
				return
			}
			ctx := typesafe.ContextWithRequestID(r.Context(), id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CorrelationID returns the correlation id in ctx, if any.
//
// The same value typesafe.RequestIDFrom returns; exposed here so a handler can
// log it without importing the core package for one call.
func CorrelationID(ctx context.Context) (string, bool) {
	id := typesafe.RequestIDFrom(ctx)
	return id, id != ""
}

func firstHeader(r *http.Request, headers []string) string {
	for _, h := range headers {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	return ""
}
