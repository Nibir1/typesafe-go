// Package gin puts a TypeSafe client in a Gin request's context.
//
//	r := gin.Default()
//	r.Use(tsgin.Middleware(client), tsgin.Correlation())
//
//	r.POST("/triage", func(c *gin.Context) {
//	    ts := tsgin.MustFrom(c)
//	    resp, err := ts.SystemOne(c.Request.Context(), req)
//	})
//
// # What this is, and is not
//
// Thin on purpose. The client goes on the *request's* context rather than only
// in Gin's key/value store, so it travels into anything that takes a
// context.Context — a repository call, a background span, a helper that knows
// nothing about Gin. Storing it only in the Gin context would strand it at the
// handler boundary.
//
// Correlation is the half worth having: it carries the inbound request id into
// the SDK, so one id ties the HTTP request, this SDK's logs and TypeSafe's own
// records together.
package gin

import (
	"github.com/gin-gonic/gin"
	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/integrations/nethttp"
)

// DefaultCorrelationHeaders are the inbound headers checked, in order.
var DefaultCorrelationHeaders = nethttp.DefaultCorrelationHeaders

// Middleware puts client on every request's context.
//
// The client is shared, not copied: *typesafe.Client is safe for concurrent
// use and is built once for the process. A per-request client would discard
// the connection pool, the circuit breaker's state and the budget's
// accounting, none of which mean anything within a single request.
func Middleware(client *typesafe.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(nethttp.NewContext(c.Request.Context(), client))
		c.Next()
	}
}

// Correlation reuses the inbound correlation id as the SDK's request id for
// every call made while serving the request.
//
// A request with no correlation header is left alone, so the client's own
// WithRequestID generator still applies rather than the id being blanked out.
func Correlation(headers ...string) gin.HandlerFunc {
	if len(headers) == 0 {
		headers = DefaultCorrelationHeaders
	}
	return func(c *gin.Context) {
		for _, h := range headers {
			if id := c.GetHeader(h); id != "" {
				c.Request = c.Request.WithContext(
					typesafe.ContextWithRequestID(c.Request.Context(), id))
				break
			}
		}
		c.Next()
	}
}

// From returns the client on the request's context, if any.
func From(c *gin.Context) (*typesafe.Client, bool) {
	if c == nil || c.Request == nil {
		return nil, false
	}
	return nethttp.From(c.Request.Context())
}

// MustFrom returns the client, panicking if Middleware is not installed.
//
// A missing client here is a wiring mistake, not a runtime condition, and
// without this it would surface as a nil dereference several frames away.
func MustFrom(c *gin.Context) *typesafe.Client {
	client, ok := From(c)
	if !ok {
		panic("typesafe/gin: no client in context; is Middleware installed?")
	}
	return client
}
