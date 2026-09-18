package typesafetest

import (
	"errors"
	"math"

	typesafe "github.com/nibir1/typesafe-go"
)

// Assertions on answers.
//
// Each reports through the test rather than returning a bool, so a failure
// names the question and shows both values without the caller writing that out
// every time. All are non-fatal (Errorf), so one wrong answer does not hide
// the others in the same response.

// AssertChoice fails unless the named Choice answer selected want.
func AssertChoice(tb TB, resp *typesafe.SystemOneResponse, id, want string) {
	tb.Helper()
	a, err := resp.Choice(id)
	if err != nil {
		tb.Errorf("choice %q: %v", id, err)
		return
	}
	if a.Choice != want {
		tb.Errorf("choice %q = %q, want %q (probabilities: %v)", id, a.Choice, want, a.Ranked())
	}
}

// AssertNoulAbove fails unless the named Noul answer exceeds threshold.
func AssertNoulAbove(tb TB, resp *typesafe.SystemOneResponse, id string, threshold float64) {
	tb.Helper()
	a, err := resp.Noul(id)
	if err != nil {
		tb.Errorf("noul %q: %v", id, err)
		return
	}
	if a.Noul <= threshold {
		tb.Errorf("noul %q = %.4f, want > %.4f", id, a.Noul, threshold)
	}
}

// AssertNoulBelow fails unless the named Noul answer is under threshold.
func AssertNoulBelow(tb TB, resp *typesafe.SystemOneResponse, id string, threshold float64) {
	tb.Helper()
	a, err := resp.Noul(id)
	if err != nil {
		tb.Errorf("noul %q: %v", id, err)
		return
	}
	if a.Noul >= threshold {
		tb.Errorf("noul %q = %.4f, want < %.4f", id, a.Noul, threshold)
	}
}

// AssertScoreBetween fails unless the named Score answer falls within [lo, hi].
func AssertScoreBetween(tb TB, resp *typesafe.SystemOneResponse, id string, lo, hi float64) {
	tb.Helper()
	a, err := resp.Score(id)
	if err != nil {
		tb.Errorf("score %q: %v", id, err)
		return
	}
	if a.Score < lo || a.Score > hi {
		tb.Errorf("score %q = %.4f, want within [%.4f, %.4f] (levels: %v)", id, a.Score, lo, hi, a.Levels())
	}
}

// AssertConfidenceAtLeast fails unless the named answer's confidence reaches
// want.
//
// A Noul fails this by construction: it carries no confidence, and asserting
// on one means the test is reading the wrong field. The message says so rather
// than comparing against a zero.
func AssertConfidenceAtLeast(tb TB, resp *typesafe.SystemOneResponse, id string, want float64) {
	tb.Helper()
	got, ok := resp.Confidence(id)
	if !ok {
		tb.Errorf("answer %q has no confidence; noul answers carry none, and their "+
			"probability already expresses the uncertainty", id)
		return
	}
	if got < want {
		tb.Errorf("confidence for %q = %.4f, want at least %.4f", id, got, want)
	}
}

// AssertProbabilitiesSumToOne fails unless every Choice and Score answer in the
// response carries a well-formed distribution.
//
// Worth running over any response built by hand: a fixture whose probabilities
// do not sum to 1 will make thresholds behave in ways that look like a bug in
// the code under test.
func AssertProbabilitiesSumToOne(tb TB, resp *typesafe.SystemOneResponse) {
	tb.Helper()
	const tol = 1e-3
	for id, a := range resp.All() {
		var sum float64
		switch v := a.(type) {
		case typesafe.ChoiceAnswer:
			for _, p := range v.Probabilities {
				sum += p
			}
		case typesafe.ScoreAnswer:
			for _, p := range v.Probabilities {
				sum += p
			}
		default:
			continue
		}
		if math.Abs(sum-1) > tol {
			tb.Errorf("probabilities for %q sum to %.6f, want 1", id, sum)
		}
	}
}

// AssertAnsweredAll fails unless the response carries an answer for every id.
func AssertAnsweredAll(tb TB, resp *typesafe.SystemOneResponse, ids ...string) {
	tb.Helper()
	for _, id := range ids {
		if _, ok := resp.Answers[id]; !ok {
			tb.Errorf("no answer for question %q", id)
		}
	}
}

// AssertErrorIs fails unless err matches target, reporting what it got instead.
func AssertErrorIs(tb TB, err, target error) {
	tb.Helper()
	if !errors.Is(err, target) {
		tb.Errorf("error = %v (%T), want it to match %v", err, err, target)
	}
}

// AssertAPIStatus fails unless err is an API error carrying want.
func AssertAPIStatus(tb TB, err error, want int) {
	tb.Helper()
	var api *typesafe.APIError
	if !errors.As(err, &api) {
		tb.Errorf("error = %v (%T), want a *typesafe.APIError with status %d", err, err, want)
		return
	}
	if api.Status != want {
		tb.Errorf("status = %d, want %d (%v)", api.Status, want, api)
	}
}
