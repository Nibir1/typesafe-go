package decision

import typesafe "github.com/nibir1/typesafe-go"

// Score composition.
//
// # Why this is not just arithmetic on ScoreAnswer.Score
//
// TypeSafe's published model-jaggedness notes are explicit that Jev's score
// levels are weakly calibrated numerically, and that you should not use a
// score to reconstruct a magnitude by interpolating between levels. A score of
// 1.6 on a three-level rubric does not mean "60% of the way from level 1 to
// level 2" in any quantity you care about.
//
// What the score *is* reliable for is ordering and thresholds. So everything
// here works on the probability distribution — the share of belief at or above
// a level — rather than on the expected value. That is a real constraint on
// the API, not a stylistic preference: the safe operations are the only ones
// offered prominently, and the unsafe one carries a warning.

// ScoreAtOrAbove returns the total probability that the answer lands at level
// or higher.
//
//	if decision.ScoreAtOrAbove(severity, 2) > 0.8 { page() }
//
// This is the sound way to threshold a Score. It reads the distribution the
// model actually produced, so it stays meaningful regardless of how the levels
// map onto any real-world quantity.
func ScoreAtOrAbove(a typesafe.ScoreAnswer, level int) float64 {
	return Clamp(a.AtOrAbove(level))
}

// ScoreAtOrBelow returns the total probability that the answer lands at level
// or lower.
func ScoreAtOrBelow(a typesafe.ScoreAnswer, level int) float64 {
	return Clamp(a.AtOrBelow(level))
}

// ScoreBetween returns the total probability of landing within [lo, hi]
// inclusive.
func ScoreBetween(a typesafe.ScoreAnswer, lo, hi int) float64 {
	if hi < lo {
		return 0
	}
	var sum float64
	for i, p := range a.LevelProbabilities() {
		if i >= lo && i <= hi {
			sum += p
		}
	}
	return Clamp(sum)
}

// ScoreExpectation returns the probability-weighted mean level index.
//
// # Read the constraint before using this
//
// This is an ordinal position, not a magnitude. TypeSafe documents that Jev's
// score levels are weakly calibrated numerically, so the distance between
// level 1 and level 2 is not comparable to the distance between level 2 and
// level 3, and neither corresponds to a fixed amount of anything.
//
// Sound uses:
//   - comparing two answers on the *same* rubric ("this ticket scored higher")
//   - a monotone input to a model you calibrate yourself
//
// Unsound uses:
//   - reading 1.6 as "1.6 units of frustration"
//   - multiplying it by a cost to get an expected cost
//   - averaging expectations from rubrics with different level counts
//
// When you want a threshold, use ScoreAtOrAbove. It is almost always what the
// caller actually meant.
func ScoreExpectation(a typesafe.ScoreAnswer) float64 { return a.Score }

// ScoreNormalized maps the expected level onto [0,1] by dividing by the
// highest level index.
//
// Carries every caveat of ScoreExpectation, plus one more: two rubrics with
// different level counts produce values on the same [0,1] scale that still are
// not comparable, because the underlying levels mean different things. Use it
// to feed a Score into a WeightedNoul composition alongside probabilities,
// where relative magnitude within one rubric is all that matters.
//
// Returns 0 for a rubric with fewer than two levels, where there is nothing to
// normalize over.
func ScoreNormalized(a typesafe.ScoreAnswer) float64 {
	n := a.NumLevels()
	if n < 2 {
		return 0
	}
	return Clamp(a.Score / float64(n-1))
}

// ScoreIsBimodal reports whether the distribution has significant mass at both
// ends and little in the middle.
//
// Worth checking before acting on a score, because a bimodal answer is the one
// case where the expected value is actively misleading: mass split between
// "harmless" and "critical" yields a mean in the middle that the model
// considers unlikely. Nothing in the score itself reveals this.
//
// A distribution is called bimodal when the lowest and highest levels each
// hold at least edgeShare of the probability, and the level nearest the mean
// holds less than either.
//
//	if decision.ScoreIsBimodal(sev, 0.3) {
//	    // The model sees two different situations here. Escalate rather than
//	    // acting on a middling score neither case supports.
//	}
func ScoreIsBimodal(a typesafe.ScoreAnswer, edgeShare float64) bool {
	ps := a.LevelProbabilities()
	if len(ps) < 3 {
		return false
	}
	low, high := ps[0], ps[len(ps)-1]
	if low < edgeShare || high < edgeShare {
		return false
	}
	mid, _ := a.Nearest()
	if mid < 0 || mid >= len(ps) {
		return false
	}
	return ps[mid] < low && ps[mid] < high
}

// ScoreConfidence returns the answer's confidence.
//
// Provided so a policy can read confidence uniformly across primitives
// without a type switch at every call site.
func ScoreConfidence(a typesafe.ScoreAnswer) float64 { return Clamp(a.Confidence) }

// ScoreThreshold is a reusable "is this at or above level L with probability
// at least P" rule.
//
//	pageOncall := decision.ScoreThreshold{Level: 2, Probability: 0.8}
//	if pageOncall.On(severity) { page() }
//
// Naming a threshold and reusing it beats scattering the same two numbers
// across a codebase, where they drift apart.
type ScoreThreshold struct {
	// Level is the rubric index to test against.
	Level int

	// Probability is the minimum share of belief at or above Level.
	Probability float64
}

// On reports whether the answer meets the threshold.
func (t ScoreThreshold) On(a typesafe.ScoreAnswer) bool {
	return ScoreAtOrAbove(a, t.Level) >= t.Probability
}

// Margin returns how far past the threshold the answer landed, negative when
// it fell short. Useful for ranking several answers that all cleared a bar.
func (t ScoreThreshold) Margin(a typesafe.ScoreAnswer) float64 {
	return ScoreAtOrAbove(a, t.Level) - t.Probability
}
