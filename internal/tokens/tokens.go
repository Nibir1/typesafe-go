// Package tokens estimates how many input tokens a request will cost.
//
// TypeSafe publishes no tokenizer, so this cannot be exact and does not
// pretend to be. What it can be is *conservative*: an estimate that is never
// below the true count is useful for refusing a request that would exceed the
// context window, while one that is sometimes below is worse than none at all,
// because it fails exactly when it matters.
//
// # The model, and where it comes from
//
// Measured against the live API over the golden contract corpus. Token count
// tracks the size of the serialized JSON closely and linearly:
//
//	tokens ≈ 239 + 0.331 × wire_bytes      (least squares, residuals within ±23)
//
// "Wire bytes" means the compact JSON the client actually sends. An earlier
// fit used the size of the pretty-printed fixture files, which are about 35%
// larger, and produced a slope that under-reported every real request. Measure
// what goes on the wire.
//
// The intercept is real and large. A request whose entire body is 125 bytes
// still costs 283 input tokens, so roughly 240 tokens are fixed per call
// regardless of content. For anyone sending many small requests this
// dominates, and an estimator that scaled purely with content would be wrong
// by an order of magnitude on exactly that workload.
//
// # Why the slope is inflated
//
// The constants used here are 240 + 0.413 × bytes: the measured intercept,
// and a slope 25% above the measured one.
//
// Fitting the tightest line that dominates every observation gives a lower
// slope and a higher intercept, which scores better on this sample and is a
// trap. A slope below the measured marginal rate only dominates because the
// larger intercept covers the difference on small inputs; extrapolated to a
// 240KB request it under-reports badly, which is precisely where a
// context-limit check has to be right.
//
// So the slope is pinned above the measured marginal rate and the intercept is
// chosen to dominate every observation at that slope. The result over-estimates
// by roughly 3% to 18% on the measured corpus, and by about 25% at scale. That
// is the correct direction to be wrong in.
//
// All measurements came from requests under 700 wire bytes. The relationship
// may change at 50KB, and nothing here can know that. Estimate.Approximate is
// always true.
package tokens

// Model constants, derived from live measurement. See the package docs.
const (
	// FixedOverhead is the per-request cost independent of content, in
	// tokens. Measured minimum was 283 for a 125-byte request.
	FixedOverhead = 240

	// BytesToTokens converts compact JSON bytes to tokens. Measured marginal
	// rate is 0.331; this carries 25% headroom so that extrapolation beyond
	// the measured range errs high.
	BytesToTokens = 0.413
)

// Estimate is a conservative token count for one request.
type Estimate struct {
	// State is the estimated cost attributable to the state.
	State int

	// PerQuestion is the estimated cost attributable to each question.
	PerQuestion map[string]int

	// Overhead is the fixed per-request cost, independent of content.
	Overhead int

	// Total is the whole request, derived from the serialized size of what
	// will actually be sent.
	Total int

	// LongestSingle is the state plus the single most expensive question,
	// including the request envelope.
	LongestSingle int

	// LongestQuestionID names the question that drove LongestSingle.
	LongestQuestionID string

	// Approximate is always true. It exists so that anything rendering an
	// estimate has to acknowledge it.
	Approximate bool
}

// Sizes carries the serialized byte counts of a request's parts.
//
// The caller marshals, because only the client knows the exact wire shape —
// the model field, the envelope keys, the question map. An earlier version of
// this package marshalled the parts itself and summed them, which silently
// omitted the envelope and made every estimate fall *below* the true count.
// That is the one direction a conservative estimator must never be wrong in,
// and it is why Total is derived from the whole serialized request rather than
// from a sum of its pieces.
type Sizes struct {
	// Full is the byte length of the complete serialized request.
	Full int

	// State is the byte length of the serialized state.
	State int

	// Questions is the byte length of each serialized question, by id.
	Questions map[string]int
}

// Of estimates the cost of a request from the sizes of its parts.
func Of(s Sizes) Estimate {
	est := Estimate{
		PerQuestion: make(map[string]int, len(s.Questions)),
		Overhead:    FixedOverhead,
		Approximate: true,
	}

	var questionBytes, longestBytes int
	for id, n := range s.Questions {
		est.PerQuestion[id] = tokensFor(n)
		questionBytes += n
		if n > longestBytes || (n == longestBytes && id < est.LongestQuestionID) {
			longestBytes = n
			est.LongestQuestionID = id
		}
	}
	est.State = tokensFor(s.State)

	// Everything in the request that is neither the state nor a question: the
	// model field, the envelope keys, the question map's own structure and its
	// ids. It is charged for, so it is counted — and counted in full against
	// the single-question limit too, since that is the conservative reading.
	envelope := s.Full - s.State - questionBytes
	if envelope < 0 {
		envelope = 0
	}

	est.Total = FixedOverhead + tokensFor(s.Full)
	est.LongestSingle = FixedOverhead + tokensFor(s.State+longestBytes+envelope)
	return est
}

// tokensFor converts a byte count to a conservative token count, rounding up.
func tokensFor(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	t := float64(bytes) * BytesToTokens
	n := int(t)
	if float64(n) < t {
		n++
	}
	return n
}
