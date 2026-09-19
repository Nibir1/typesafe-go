// Package fiber puts a TypeSafe client in a Fiber v3 request's context.
//
//	app := fiber.New()
//	app.Use(tsfiber.Middleware(client), tsfiber.Correlation())
//
//	app.Post("/triage", func(c fiber.Ctx) error {
//	    ts := tsfiber.MustFrom(c)
//	    resp, err := ts.SystemOne(c.Context(), req)
//	})
//
// # Fiber is not net/http, and it matters here
//
// Fiber runs on fasthttp, so there is no *http.Request to hang a context on.
// Fiber v3 provides Ctx.Context() and Ctx.SetContext() for exactly this, and
// that is what these use.
//
// The consequence worth knowing: **pass c.Context() to SystemOne**, not
// context.Background(). Fiber's context is the one that carries the client and
// the correlation id, and it is also the one canceled when the client hangs
// up — which is what stops an abandoned request from continuing to spend
// tokens.
package fiber

import (
	"github.com/gofiber/fiber/v3"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/integrations/nethttp"
)

// DefaultCorrelationHeaders are the inbound headers checked, in order.
var DefaultCorrelationHeaders = nethttp.DefaultCorrelationHeaders

// Middleware puts client on every request's context.
func Middleware(client *typesafe.Client) fiber.Handler {
	return func(c fiber.Ctx) error {
		c.SetContext(nethttp.NewContext(c.Context(), client))
		return c.Next()
	}
}

// Correlation reuses the inbound correlation id as the SDK's request id.
//
// A request with no correlation header is left alone, so the client's own
// WithRequestID generator still applies.
func Correlation(headers ...string) fiber.Handler {
	if len(headers) == 0 {
		headers = DefaultCorrelationHeaders
	}
	return func(c fiber.Ctx) error {
		for _, h := range headers {
			if id := c.Get(h); id != "" {
				c.SetContext(typesafe.ContextWithRequestID(c.Context(), id))
				break
			}
		}
		return c.Next()
	}
}

// From returns the client on the request's context, if any.
func From(c fiber.Ctx) (*typesafe.Client, bool) {
	if c == nil {
		return nil, false
	}
	return nethttp.From(c.Context())
}

// MustFrom returns the client, panicking if Middleware is not installed.
func MustFrom(c fiber.Ctx) *typesafe.Client {
	client, ok := From(c)
	if !ok {
		panic("typesafe/fiber: no client in context; is Middleware installed?")
	}
	return client
}
