package typesafe

import "context"

// Result is the outcome of an asynchronous call: exactly one of Response and
// Err is non-nil.
type Result struct {
	// Response is the answer set, nil on failure.
	Response *SystemOneResponse

	// Err is the failure, nil on success.
	Err error
}

// SystemOneAsync starts a call and returns a channel that will carry its one
// result.
//
//	a := client.SystemOneAsync(ctx, reqA)
//	b := client.SystemOneAsync(ctx, reqB)
//	ra, rb := <-a, <-b
//
// For fanning out over several states without writing the goroutine and
// channel plumbing each time. Every question about *one* state belongs in a
// single request — they are evaluated in parallel server-side and cost only
// the extra tokens — so this is for fanning out over different states, not
// different questions.
//
// # It cannot leak
//
// The channel is buffered with room for the single result, so the goroutine
// always completes its send and exits even if nobody ever reads. That matters
// more than it sounds: the obvious unbuffered implementation leaks a goroutine
// for every abandoned call, and abandoning calls is exactly what happens when
// a caller takes the first of several results and returns.
//
// The channel is closed after the send, so a range over it terminates and a
// second receive yields the zero Result rather than blocking.
//
// Cancelling ctx does not close the channel early — the in-flight request is
// cancelled, and the resulting error arrives on the channel as a normal
// result. A caller waiting on the channel is therefore always woken exactly
// once, whether the call succeeded, failed, or was cancelled.
func (c *Client) SystemOneAsync(ctx context.Context, req *SystemOneRequest) <-chan Result {
	ch := make(chan Result, 1)
	go func() {
		defer close(ch)
		resp, err := c.SystemOne(ctx, req)
		ch <- Result{Response: resp, Err: err}
	}()
	return ch
}

// SystemOneAll runs several requests concurrently and returns their results in
// input order.
//
//	results := client.SystemOneAll(ctx, reqA, reqB, reqC)
//	for i, r := range results {
//	    if r.Err != nil { ... }
//	}
//
// Results are positional: results[i] belongs to requests[i], regardless of
// which finished first. A failure in one does not affect the others — each
// carries its own error — because a batch where one bad input discards the
// other ninety-nine results is not useful.
//
// This is the unbounded form, appropriate for a handful of requests. For
// thousands, with a worker pool and rate-limit awareness, use the batch API.
func (c *Client) SystemOneAll(ctx context.Context, requests ...*SystemOneRequest) []Result {
	if len(requests) == 0 {
		return nil
	}

	results := make([]Result, len(requests))
	chans := make([]<-chan Result, len(requests))
	for i, req := range requests {
		chans[i] = c.SystemOneAsync(ctx, req)
	}
	// Collect in order. Every goroutine sends into a buffered channel, so
	// reading them sequentially cannot deadlock or hold any of them up.
	for i, ch := range chans {
		results[i] = <-ch
	}
	return results
}
