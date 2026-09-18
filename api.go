package typesafe

import "context"

// API is the behavior *Client provides.
//
// Go convention says consumers declare the interfaces they need, and for most
// code that remains the better habit — depend on a one-method interface you
// define at the point of use, not on everything a client can do.
//
// This one is provided because writing it out is otherwise the first thing
// every caller does, and because typesafetest.Mock needs a shared shape to
// satisfy. It is deliberately small and will not grow: methods added to
// *Client in later phases stay off this interface unless they are part of the
// core request path, so that implementing it never becomes a burden.
//
//	func triage(ctx context.Context, api typesafe.API, ticket Ticket) (Queue, error) {
//	    resp, err := api.SystemOne(ctx, &typesafe.SystemOneRequest{ ... })
//	    ...
//	}
//
// Both *Client and typesafetest.Mock satisfy it, so the same function can be
// exercised against a mock, a test server, a cassette, or the live API without
// changing its signature.
type API interface {
	// SystemOne evaluates one state against a map of named questions.
	SystemOne(ctx context.Context, req *SystemOneRequest) (*SystemOneResponse, error)

	// Models lists the model names this account may use.
	Models(ctx context.Context) ([]ModelCard, error)
}

// Compile-time assertion that the real client satisfies the interface it
// publishes. Without this, a signature change here would only be caught by
// whichever test happened to assign one to the other.
var _ API = (*Client)(nil)
