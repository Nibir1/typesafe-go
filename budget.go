package typesafe

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nibir1/typesafe-go/internal/tokens"
)

// Jev 1.13 limits, from the published Models page.
//
// Every one of these is a **configurable default, not a constant**. TypeSafe
// states plainly that rate limits "can change without notice" during early
// access, so hard-coding them into the client's behavior would mean shipping a
// new release every time they move. They are starting values for a Budget the
// caller owns.
const (
	// MaxContextTokens is the ceiling on state plus every question combined.
	MaxContextTokens = 64_000

	// MaxSingleQuestionTokens is the ceiling on state plus the single longest
	// question. This is a *separate* limit, and the one that is easy to miss:
	// a request can pass the 64k check and fail this one.
	MaxSingleQuestionTokens = 32_000

	// DefaultRequestsPerMinute is the published request rate limit.
	DefaultRequestsPerMinute = 1_200

	// DefaultTokensPerSecond is the published token rate limit.
	DefaultTokensPerSecond = 250_000

	// DefaultInputCostPerMillionTokens is the published price in US dollars.
	// Output tokens are free.
	DefaultInputCostPerMillionTokens = 0.042
)

// TokenEstimate is a conservative guess at what a request will cost.
//
// # It is an estimate, and it says so
//
// TypeSafe publishes no tokenizer. This is fitted from measurements against
// the live API: token count tracks serialized JSON size closely and linearly,
// with a large fixed overhead of roughly 250 tokens per call regardless of
// content. The model carries 25% headroom on the marginal rate so that it errs
// high rather than low — an estimate that is sometimes under is worse than
// none, because it fails exactly when a request is near the limit.
//
// Expect it to over-report by roughly a quarter. Do not use it for billing.
type TokenEstimate struct {
	// State is the estimated cost of the state alone.
	State int

	// PerQuestion is the estimated cost of each question, by id.
	PerQuestion map[string]int

	// Overhead is the fixed per-request cost, independent of content.
	Overhead int

	// Total is the whole request: compare against MaxContextTokens.
	Total int

	// LongestSingle is state plus the most expensive single question:
	// compare against MaxSingleQuestionTokens.
	LongestSingle int

	// LongestQuestionID names the question driving LongestSingle.
	LongestQuestionID string

	// ExceedsTotal reports that Total is over MaxContextTokens.
	ExceedsTotal bool

	// ExceedsSingle reports that LongestSingle is over
	// MaxSingleQuestionTokens. A request can pass ExceedsTotal and fail this.
	ExceedsSingle bool

	// Approximate is always true.
	Approximate bool
}

// WouldExceedLimits reports whether either documented ceiling is crossed.
func (e TokenEstimate) WouldExceedLimits() bool { return e.ExceedsTotal || e.ExceedsSingle }

// Err returns a descriptive error when a limit is crossed, or nil.
//
// The message names which ceiling, by how much, and which question is
// responsible — because "request too large" sends the reader back to count
// bytes by hand.
func (e TokenEstimate) Err() error {
	switch {
	case e.ExceedsTotal:
		return fmt.Errorf(
			"%w: estimated %d tokens for state plus all questions, above the %d limit "+
				"(estimate is conservative and may over-report by ~25%%)",
			ErrInvalidRequest, e.Total, MaxContextTokens)
	case e.ExceedsSingle:
		return fmt.Errorf(
			"%w: estimated %d tokens for state plus question %q, above the %d "+
				"single-question limit — note this is a separate ceiling from the %d "+
				"whole-request one, which this request is within",
			ErrInvalidRequest, e.LongestSingle, e.LongestQuestionID,
			MaxSingleQuestionTokens, MaxContextTokens)
	default:
		return nil
	}
}

// String renders the estimate for a terminal.
func (e TokenEstimate) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "~%d tokens (approximate, conservative)\n", e.Total)
	fmt.Fprintf(&b, "  fixed overhead  %6d\n", e.Overhead)
	fmt.Fprintf(&b, "  state           %6d\n", e.State)

	ids := make([]string, 0, len(e.PerQuestion))
	for id := range e.PerQuestion {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if e.PerQuestion[ids[i]] != e.PerQuestion[ids[j]] {
			return e.PerQuestion[ids[i]] > e.PerQuestion[ids[j]]
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		fmt.Fprintf(&b, "  %-15s %6d\n", id, e.PerQuestion[id])
	}

	fmt.Fprintf(&b, "  longest single  %6d / %d\n", e.LongestSingle, MaxSingleQuestionTokens)
	fmt.Fprintf(&b, "  total           %6d / %d\n", e.Total, MaxContextTokens)
	if e.WouldExceedLimits() {
		fmt.Fprintf(&b, "  OVER LIMIT: %v\n", e.Err())
	}
	return b.String()
}

// EstimateTokens returns a conservative estimate of the request's input cost.
//
//	est := req.EstimateTokens()
//	if err := est.Err(); err != nil {
//	    return err // caught before a round trip
//	}
//
// Both documented ceilings are checked. The single-question one is the trap:
// a request comfortably inside the 64k whole-request budget can still be
// rejected for a state plus one long question exceeding 32k, and nothing in
// the error the server returns explains which limit was hit.
func (r *SystemOneRequest) EstimateTokens() TokenEstimate {
	if r == nil {
		return TokenEstimate{Approximate: true, Overhead: tokens.FixedOverhead}
	}

	// Marshal exactly what will be sent, so the estimate counts the envelope,
	// the model field and the question ids — all of which are charged for.
	sizes := tokens.Sizes{Questions: make(map[string]int, len(r.Questions))}
	if b, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}{r.State, firstNonEmpty(r.Model, DefaultModel), r.Questions}); err == nil {
		sizes.Full = len(b)
	}
	if b, err := json.Marshal(r.State); err == nil {
		sizes.State = len(b)
	}
	for id, q := range r.Questions {
		if b, err := json.Marshal(q); err == nil {
			sizes.Questions[id] = len(b)
		}
	}

	raw := tokens.Of(sizes)
	return TokenEstimate{
		State:             raw.State,
		PerQuestion:       raw.PerQuestion,
		Overhead:          raw.Overhead,
		Total:             raw.Total,
		LongestSingle:     raw.LongestSingle,
		LongestQuestionID: raw.LongestQuestionID,
		ExceedsTotal:      raw.Total > MaxContextTokens,
		ExceedsSingle:     raw.LongestSingle > MaxSingleQuestionTokens,
		Approximate:       true,
	}
}

// CostEstimate is a pre-flight guess at what a request will cost in money.
type CostEstimate struct {
	// Tokens is the underlying token estimate.
	Tokens TokenEstimate

	// Questions is how many questions the request asks.
	Questions int

	// InputCostUSD is the estimated charge. Output tokens are free.
	//
	// Derived from a conservative token estimate, so it over-reports. Use it
	// to size a workload, never to reconcile a bill.
	InputCostUSD float64

	// RatePerMillionUSD is the price used.
	RatePerMillionUSD float64
}

// String renders the cost estimate, always flagged as approximate.
func (c CostEstimate) String() string {
	return fmt.Sprintf("~%d tokens, ~$%.6f at $%.3f/Mtok (approximate; over-reports by design)",
		c.Tokens.Total, c.InputCostUSD, c.RatePerMillionUSD)
}

// EstimateCost estimates the input charge for a request.
//
// ratePerMillionUSD is the price per million input tokens. Pass 0 to use
// DefaultInputCostPerMillionTokens, which is the published Jev 1.13 rate —
// but pass your own if you have negotiated terms, because a hard-coded price
// is wrong the moment anyone's contract differs.
//
//	cost := req.EstimateCost(0)
//	log.Printf("%s", cost) // never present this as authoritative
func (r *SystemOneRequest) EstimateCost(ratePerMillionUSD float64) CostEstimate {
	if ratePerMillionUSD <= 0 {
		ratePerMillionUSD = DefaultInputCostPerMillionTokens
	}
	est := r.EstimateTokens()
	n := 0
	if r != nil {
		n = len(r.Questions)
	}
	return CostEstimate{
		Tokens:            est,
		Questions:         n,
		InputCostUSD:      float64(est.Total) / 1_000_000 * ratePerMillionUSD,
		RatePerMillionUSD: ratePerMillionUSD,
	}
}
