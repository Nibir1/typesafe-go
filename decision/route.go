package decision

import (
	"fmt"
	"sort"

	typesafe "github.com/nibir1/typesafe-go"
)

// Threshold is a named probability cutoff.
//
//	const spamThreshold = decision.Threshold(0.85)
//	if spamThreshold.On(isSpam) { quarantine() }
//
// The type exists to make the number nameable. A bare 0.85 inlined at three
// call sites becomes 0.85, 0.8 and 0.9 within a year, and nothing catches it.
type Threshold float64

// On reports whether a Noul answer meets the threshold.
func (t Threshold) On(a typesafe.NoulAnswer) bool { return a.Noul >= float64(t) }

// Value reports whether a bare probability meets the threshold.
func (t Threshold) Value(p float64) bool { return p >= float64(t) }

// Margin returns how far past the threshold the answer landed, negative when
// it fell short.
func (t Threshold) Margin(a typesafe.NoulAnswer) float64 { return a.Noul - float64(t) }

// Band is what a confidence level permits.
type Band int

// The three bands from TypeSafe's confidence guidance.
const (
	// Escalate means do not act: route to a person, ask for clarification, or
	// fall back to another system.
	Escalate Band = iota

	// Confirm means the answer is probably right but the stakes warrant a
	// check — a user confirmation, a second opinion, a review queue.
	Confirm

	// Act means proceed automatically.
	Act
)

func (b Band) String() string {
	switch b {
	case Act:
		return "act"
	case Confirm:
		return "confirm"
	case Escalate:
		return "escalate"
	default:
		return "unknown"
	}
}

// Bands implements the three-path pattern TypeSafe's confidence page
// recommends: act automatically when confident, proceed with a check when
// moderately confident, and refuse to act when the model is telling you it
// does not know.
//
//	routing := decision.Bands{ActAbove: 0.9, ConfirmAbove: 0.5}
//
//	switch routing.Classify(department) {
//	case decision.Act:      route(department.Choice)
//	case decision.Confirm:  askUser(department.Choice)
//	case decision.Escalate: routeToHuman()
//	}
//
// The right boundaries depend on what a wrong answer costs. Showing the wrong
// screen and approving the wrong transfer do not deserve the same bar, and the
// docs are explicit that a single global threshold is the wrong shape for this.
type Bands struct {
	// ActAbove is the confidence at or above which to act automatically.
	ActAbove float64

	// ConfirmAbove is the confidence at or above which to act with a check.
	ConfirmAbove float64
}

// Validate reports whether the bands are ordered sensibly.
func (b Bands) Validate() error {
	if b.ConfirmAbove > b.ActAbove {
		return fmt.Errorf("decision: ConfirmAbove (%.3f) is above ActAbove (%.3f), "+
			"which would make the Confirm band unreachable", b.ConfirmAbove, b.ActAbove)
	}
	return nil
}

// Classify places a confidence value into a band.
func (b Bands) Classify(confidence float64) Band {
	switch {
	case confidence >= b.ActAbove:
		return Act
	case confidence >= b.ConfirmAbove:
		return Confirm
	default:
		return Escalate
	}
}

// ClassifyAnswer places an answer into a band by its confidence.
//
// A Noul answer returns an error rather than a band. Noul answers carry no
// confidence — the probability itself is the uncertainty — so classifying one
// by confidence would be reading a zero and escalating every call. Threshold
// the probability with Uncertain or Threshold instead.
func (b Bands) ClassifyAnswer(a typesafe.Answer) (Band, error) {
	switch v := a.(type) {
	case typesafe.ChoiceAnswer:
		return b.Classify(v.Confidence), nil
	case typesafe.ScoreAnswer:
		return b.Classify(v.Confidence), nil
	case typesafe.NoulAnswer:
		return Escalate, fmt.Errorf(
			"decision: a noul answer carries no confidence, so it cannot be banded; "+
				"threshold its probability (%.4f) instead", v.Noul)
	default:
		return Escalate, fmt.Errorf("decision: cannot band a %T", a)
	}
}

// Uncertain reports whether a Noul probability sits within delta of 0.5, the
// region where the model is expressing genuine ignorance rather than a weak
// opinion.
//
// This is the Noul equivalent of low confidence, and the right way to gate on
// a yes/no answer you do not trust.
func Uncertain(p, delta float64) bool {
	return p > 0.5-delta && p < 0.5+delta
}

// Router maps Choice options onto destinations of any type.
//
//	queues := decision.NewRouter(map[string]string{
//	    "billing":   "queue-billing",
//	    "technical": "queue-eng",
//	}).WithFallback("queue-triage")
//
//	queue, _ := queues.Route(department)
//
// Generic in the destination so it can carry a queue name, a handler func, or
// a struct — whatever your code routes to — without stringly-typed lookups on
// the other side.
type Router[T any] struct {
	routes      map[string]T
	fallback    T
	hasFallback bool
	minConf     float64
}

// NewRouter builds a router over a set of option-to-destination mappings.
func NewRouter[T any](routes map[string]T) Router[T] {
	return Router[T]{routes: routes}
}

// WithFallback returns a copy that routes unmapped options to dst instead of
// failing.
func (r Router[T]) WithFallback(dst T) Router[T] {
	r.fallback = dst
	r.hasFallback = true
	return r
}

// RequireConfidence returns a copy that refuses to route an answer below min,
// so a low-confidence classification cannot silently land in a queue.
func (r Router[T]) RequireConfidence(min float64) Router[T] {
	r.minConf = min
	return r
}

// Route resolves a Choice answer to a destination.
func (r Router[T]) Route(a typesafe.ChoiceAnswer) (T, error) {
	var zero T

	if r.minConf > 0 && a.Confidence < r.minConf {
		if r.hasFallback {
			return r.fallback, nil
		}
		return zero, fmt.Errorf(
			"decision: confidence %.4f for %q is below the required %.4f and there is no fallback",
			a.Confidence, a.Choice, r.minConf)
	}

	if dst, ok := r.routes[a.Choice]; ok {
		return dst, nil
	}
	if r.hasFallback {
		return r.fallback, nil
	}
	return zero, fmt.Errorf("decision: no route for option %q (known: %v)", a.Choice, r.options())
}

// Options returns the routed option names, sorted.
func (r Router[T]) Options() []string { return r.options() }

func (r Router[T]) options() []string {
	out := make([]string, 0, len(r.routes))
	for k := range r.routes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Exhaustive reports any option the answer's distribution contains that the
// router has no route for.
//
// Worth asserting in a test: a Choice question and its router are two lists
// that must agree, and nothing in the type system keeps them in step. Adding
// an option to the question without adding a route is the exact failure this
// catches, and in production it shows up as a fallback silently swallowing a
// category.
func (r Router[T]) Exhaustive(a typesafe.ChoiceAnswer) []string {
	var missing []string
	for opt := range a.Probabilities {
		if _, ok := r.routes[opt]; !ok {
			missing = append(missing, opt)
		}
	}
	sort.Strings(missing)
	return missing
}
