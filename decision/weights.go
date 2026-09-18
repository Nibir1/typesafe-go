package decision

import (
	"fmt"
	"sort"
)

// Weights maps a question id to its coefficient in a composite score.
//
// This is the pattern TypeSafe's Composite scoring page describes: break a
// broad judgment into atomic questions, then combine them with weights your
// code owns. The weights are the part you tune; the model's job is only to
// answer each narrow question well.
type Weights map[string]float64

// WeightedNoul composes Noul answers into a single value.
//
//	risk := decision.WeightedNoul(decision.Weights{
//	    "asks_for_credentials":  0.4,
//	    "creates_time_pressure": 0.3,
//	    "generic_greeting":      0.1,
//	})
//	score, err := risk.Apply(resp)
//
// By default the result is normalized by the sum of the weights, so it lands
// in [0,1] regardless of whether the weights happen to sum to 1. That makes a
// policy's thresholds stable when you add a signal — the alternative is that
// every threshold silently shifts the moment the weights change.
//
// Use Unnormalized when you want the raw weighted sum, for instance when the
// weights are calibrated log-odds rather than a convex combination.
type WeightedNoul struct {
	weights    Weights
	normalize  bool
	missing    MissingPolicy
	defaultVal float64
}

// MissingPolicy says what to do when a weighted question has no answer.
type MissingPolicy int

const (
	// MissingIsError fails the composition. The default, because a weight
	// naming a question nobody asked is almost always a typo or a request
	// that drifted from its policy, and silently scoring it as zero turns
	// that mistake into a plausible-looking number.
	MissingIsError MissingPolicy = iota

	// MissingIsZero treats an absent answer as 0 and keeps its weight in the
	// denominator, lowering the score.
	MissingIsZero

	// MissingIsSkipped drops the term entirely, removing its weight from the
	// denominator so the remaining signals are scored among themselves.
	// Appropriate when questions are asked speculatively and some may not
	// apply.
	MissingIsSkipped

	// MissingIsDefault substitutes a fixed value; see WithDefault.
	MissingIsDefault
)

// NewWeightedNoul builds a normalized weighted composition.
func NewWeightedNoul(w Weights) WeightedNoul {
	return WeightedNoul{weights: w, normalize: true}
}

// Unnormalized returns a copy that reports the raw weighted sum rather than
// dividing by the total weight.
func (w WeightedNoul) Unnormalized() WeightedNoul {
	w.normalize = false
	return w
}

// OnMissing returns a copy using the given policy for absent answers.
func (w WeightedNoul) OnMissing(p MissingPolicy) WeightedNoul {
	w.missing = p
	return w
}

// WithDefault returns a copy that substitutes v for absent answers.
func (w WeightedNoul) WithDefault(v float64) WeightedNoul {
	w.missing = MissingIsDefault
	w.defaultVal = Clamp(v)
	return w
}

// Weights returns a copy of the coefficients.
func (w WeightedNoul) Weights() Weights {
	out := make(Weights, len(w.weights))
	for k, v := range w.weights {
		out[k] = v
	}
	return out
}

// Apply composes the answers into a single value.
func (w WeightedNoul) Apply(src Source) (float64, error) {
	t, err := w.Explain(src)
	if err != nil {
		return 0, err
	}
	return t.Total, nil
}

// MustApply is Apply, panicking on error.
//
// For a policy whose weights are compile-time constants and whose questions
// are asked in the same function — where a missing answer is a programming
// mistake rather than a runtime condition. Prefer Apply anywhere the answers
// come from somewhere you do not control.
func (w WeightedNoul) MustApply(src Source) float64 {
	v, err := w.Apply(src)
	if err != nil {
		panic(err)
	}
	return v
}

// Explain composes the answers and returns the full accounting.
//
// Iteration is over sorted question ids, so the resulting Trace is identical
// across runs. Go randomizes map order, and a trace that reorders itself
// between two runs of the same input is useless for diffing an audit log.
func (w WeightedNoul) Explain(src Source) (Trace, error) {
	ids := make([]string, 0, len(w.weights))
	for id := range w.weights {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var (
		terms     []Term
		total     float64
		weightSum float64
	)
	for _, id := range ids {
		weight := w.weights[id]

		ans, err := src.Noul(id)
		value := ans.Noul
		if err != nil {
			switch w.missing {
			case MissingIsSkipped:
				continue
			case MissingIsZero:
				value = 0
			case MissingIsDefault:
				value = w.defaultVal
			default:
				return Trace{}, fmt.Errorf("decision: weighted composition: %w", err)
			}
		}

		value = Clamp(value)
		contribution := weight * value
		total += contribution
		weightSum += weight
		terms = append(terms, Term{
			QuestionID:   id,
			Weight:       weight,
			Value:        value,
			Contribution: contribution,
		})
	}

	trace := Trace{Terms: terms, Total: total, WeightSum: weightSum, Normalized: w.normalize}
	if w.normalize && weightSum != 0 {
		trace.Total = total / weightSum
	}
	sortTerms(trace.Terms)
	return trace, nil
}
