// Package echo puts a TypeSafe client in an Echo request's context.
//
//	e := echo.New()
//	e.Use(tsecho.Middleware(client), tsecho.Correlation())
//
//	e.POST("/triage", func(c echo.Context) error {
//	    ts := tsecho.MustFrom(c)
//	    resp, err := ts.SystemOne(c.Request().Context(), req)
//	})
//
// The client goes on the *request's* context rather than only in Echo's own
// store, so it travels into anything taking a context.Context. See the gin
// integration's documentation for the reasoning, which is identical.
package echo

import (
	"github.com/labstack/echo/v4"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/integrations/nethttp"
)

// DefaultCorrelationHeaders are the inbound headers checked, in order.
var DefaultCorrelationHeaders = nethttp.DefaultCorrelationHeaders

// Middleware puts client on every request's context.
func Middleware(client *typesafe.Client) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			r := c.Request()
			c.SetRequest(r.WithContext(nethttp.NewContext(r.Context(), client)))
			return next(c)
		}
	}
}

// Correlation reuses the inbound correlation id as the SDK's request id.
//
// A request with no correlation header is left alone, so the client's own
// WithRequestID generator still applies.
func Correlation(headers ...string) echo.MiddlewareFunc {
	if len(headers) == 0 {
		headers = DefaultCorrelationHeaders
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			r := c.Request()
			for _, h := range headers {
				if id := r.Header.Get(h); id != "" {
					c.SetRequest(r.WithContext(typesafe.ContextWithRequestID(r.Context(), id)))
					break
				}
			}
			return next(c)
		}
	}
}

// From returns the client on the request's context, if any.
func From(c echo.Context) (*typesafe.Client, bool) {
	if c == nil || c.Request() == nil {
		return nil, false
	}
	return nethttp.From(c.Request().Context())
}

// MustFrom returns the client, panicking if Middleware is not installed.
func MustFrom(c echo.Context) *typesafe.Client {
	client, ok := From(c)
	if !ok {
		panic("typesafe/echo: no client in context; is Middleware installed?")
	}
	return client
}
