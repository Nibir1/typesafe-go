// Package decision composes System One answers into decisions.
//
// TypeSafe's documentation is consistent on this point: decompose a broad
// judgment into atomic questions, ask them together, and combine the answers
// with deterministic logic in code. The Composite scoring and Confidence-gated
// routing pattern pages both say so.
//
// What none of them ship is the code. Every SDK — official and community alike
// — hands back a probability distribution and stops. This package is the part
// that was left as an exercise.
//
// # Nothing here touches the network
//
// Every function is pure arithmetic over numbers you already have. The package
// does not import the client, cannot make a request, and needs no key to test.
// Given identical inputs it returns identical outputs, which is what makes a
// decision auditable: you can replay last Tuesday's answers and get last
// Tuesday's verdict.
//
// # The independence assumption
//
// The boolean operators treat probabilities as independent events. That is
// exactly right when the questions are genuinely about different things:
//
//	decision.And(0.9, 0.8) // 0.72
//
// It is wrong when two questions are near-restatements of each other. Ask "is
// this urgent?" and "is this time-sensitive?" about the same state and you get
// two highly correlated answers; multiplying them understates the truth badly,
// because the real joint probability is close to the smaller of the two rather
// than their product.
//
// The fix is not a different formula — it is one question instead of two. When
// you genuinely need a conservative bound over answers that may be correlated,
// use MinAll and MaxAny, which make no independence assumption at all.
package decision

import "math"

// Clamp confines p to [0,1].
//
// Probabilities arriving from a model are already in range, but hand-built
// test fixtures and arithmetic on weights are not always so careful, and a
// value outside [0,1] propagating through a policy produces a verdict that
// looks plausible and is meaningless.
func Clamp(p float64) float64 {
	switch {
	case math.IsNaN(p):
		return 0
	case p < 0:
		return 0
	case p > 1:
		return 1
	default:
		return p
	}
}

// Not returns the probability that something is false.
//
//	Not(0.92) // 0.08
func Not(p float64) float64 { return 1 - Clamp(p) }

// And returns the probability that both hold, assuming independence.
//
//	And(0.9, 0.8) // 0.72
//
// See the package documentation on when independence is a safe assumption.
// For a conservative bound over possibly-correlated answers, use MinAll.
func And(a, b float64) float64 { return Clamp(a) * Clamp(b) }

// Or returns the probability that at least one holds, assuming independence.
//
//	Or(0.9, 0.8) // 0.98
//
// This is the inclusive or: a + b - ab, not a + b. Adding probabilities is the
// most common way to end up with a "probability" above 1.
func Or(a, b float64) float64 {
	x, y := Clamp(a), Clamp(b)
	return x + y - x*y
}

// All returns the probability that every one holds, assuming independence.
//
// Empty input returns 1: nothing can fail when there is nothing to check, and
// this keeps All the identity for And.
func All(ps ...float64) float64 {
	out := 1.0
	for _, p := range ps {
		out *= Clamp(p)
	}
	return out
}

// Any returns the probability that at least one holds, assuming independence.
//
// Empty input returns 0, the identity for Or.
//
// Computed as 1 - All(Not(p)...), which is numerically better behaved than
// folding Or across the list when many probabilities are small.
func Any(ps ...float64) float64 {
	out := 1.0
	for _, p := range ps {
		out *= 1 - Clamp(p)
	}
	return 1 - out
}

// MinAll is a conservative lower bound on All that assumes nothing about
// independence.
//
// Where All(0.9, 0.9, 0.9) is 0.729, MinAll is 0.9. If the three questions are
// really asking the same thing three ways, 0.9 is the honest answer and 0.729
// is an artifact of pretending otherwise.
//
// Empty input returns 1.
func MinAll(ps ...float64) float64 {
	out := 1.0
	for _, p := range ps {
		if c := Clamp(p); c < out {
			out = c
		}
	}
	return out
}

// MaxAny is a conservative lower bound on Any that assumes nothing about
// independence.
//
// Empty input returns 0.
func MaxAny(ps ...float64) float64 {
	out := 0.0
	for _, p := range ps {
		if c := Clamp(p); c > out {
			out = c
		}
	}
	return out
}

// AtLeast returns the probability that at least n of the given independent
// events occur.
//
//	AtLeast(2, 0.5, 0.5, 0.5) // 0.5
//
// This is the upper tail of a Poisson-binomial distribution, computed exactly
// by dynamic programming rather than approximated. It answers the question
// "how likely is it that several of these warning signs are real?", which a
// weighted sum cannot: a sum of 1.5 does not distinguish three half-certain
// signals from one certain signal and one absent.
//
// n <= 0 returns 1. n greater than the number of events returns 0.
func AtLeast(n int, ps ...float64) float64 {
	if n <= 0 {
		return 1
	}
	if n > len(ps) {
		return 0
	}

	// dp[k] is the probability that exactly k of the events seen so far occur.
	dp := make([]float64, len(ps)+1)
	dp[0] = 1
	for i, p := range ps {
		c := Clamp(p)
		// Descend so each dp[k] is still the previous iteration's value when
		// read.
		for k := i + 1; k >= 1; k-- {
			dp[k] = dp[k]*(1-c) + dp[k-1]*c
		}
		dp[0] *= 1 - c
	}

	var sum float64
	for k := n; k < len(dp); k++ {
		sum += dp[k]
	}
	return Clamp(sum)
}

// Exactly returns the probability that exactly n of the given independent
// events occur.
func Exactly(n int, ps ...float64) float64 {
	if n < 0 || n > len(ps) {
		return 0
	}
	dp := make([]float64, len(ps)+1)
	dp[0] = 1
	for i, p := range ps {
		c := Clamp(p)
		for k := i + 1; k >= 1; k-- {
			dp[k] = dp[k]*(1-c) + dp[k-1]*c
		}
		dp[0] *= 1 - c
	}
	return Clamp(dp[n])
}

// Expected returns the expected number of events that occur.
//
// Simply the sum of the probabilities, and unlike AtLeast it makes no
// independence assumption — expectation is linear regardless. Useful as a
// cheap severity signal when you care how much is wrong rather than whether a
// specific combination is.
func Expected(ps ...float64) float64 {
	var sum float64
	for _, p := range ps {
		sum += Clamp(p)
	}
	return sum
}
