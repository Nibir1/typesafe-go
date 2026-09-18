package decision

import (
	"fmt"
	"sort"

	typesafe "github.com/nibir1/typesafe-go"
)

// Errors returned when a composition names a question the response does not
// answer, or answers with a different primitive.
var (
	// ErrMissingAnswer means a weight or rule named a question id that is not
	// in the response.
	ErrMissingAnswer = fmt.Errorf("decision: response has no answer for that question")

	// ErrWrongType means the answer exists but is a different primitive than
	// the composition needs.
	ErrWrongType = fmt.Errorf("decision: answer is the wrong primitive for this composition")
)

// Source is where a composition reads answers from.
//
// *typesafe.SystemOneResponse satisfies it, and so does a plain map, which
// makes a policy testable without constructing a response:
//
//	decision.Nouls{"is_spam": 0.93, "has_link": 0.80}
type Source interface {
	// Noul returns the probability for a Noul answer.
	Noul(id string) (typesafe.NoulAnswer, error)

	// Choice returns a Choice answer.
	Choice(id string) (typesafe.ChoiceAnswer, error)

	// Score returns a Score answer.
	Score(id string) (typesafe.ScoreAnswer, error)
}

// Nouls is a bare map of question id to probability, satisfying Source.
//
// Policies are worth unit-testing over a table of inputs, and building a full
// SystemOneResponse for each row is friction that discourages it:
//
//	for _, tc := range cases {
//	    got := SpamPolicy.Evaluate(decision.Nouls(tc.answers))
//	}
type Nouls map[string]float64

// Noul implements Source.
func (n Nouls) Noul(id string) (typesafe.NoulAnswer, error) {
	p, ok := n[id]
	if !ok {
		return typesafe.NoulAnswer{}, fmt.Errorf("%w: %q", ErrMissingAnswer, id)
	}
	return typesafe.NoulAnswer{Noul: p}, nil
}

// Choice implements Source, and always fails: a Nouls map holds no choices.
func (n Nouls) Choice(id string) (typesafe.ChoiceAnswer, error) {
	return typesafe.ChoiceAnswer{}, fmt.Errorf("%w: %q is a noul", ErrWrongType, id)
}

// Score implements Source, and always fails: a Nouls map holds no scores.
func (n Nouls) Score(id string) (typesafe.ScoreAnswer, error) {
	return typesafe.ScoreAnswer{}, fmt.Errorf("%w: %q is a noul", ErrWrongType, id)
}

// Compile-time checks that the real response and the test double agree.
var (
	_ Source = (*typesafe.SystemOneResponse)(nil)
	_ Source = Nouls(nil)
)

// Term is one question's contribution to a composed result, as recorded by
// Explain.
type Term struct {
	// QuestionID is the answer this term read.
	QuestionID string

	// Weight is the coefficient applied to it.
	Weight float64

	// Value is the probability the answer carried.
	Value float64

	// Contribution is Weight * Value: what this term added to the total.
	Contribution float64
}

// Trace is the full accounting behind a composed result.
//
// This exists because a probability that appears in an audit log without its
// derivation is not evidence of anything. When a decision is questioned six
// months later — by a customer, a regulator, or the person debugging it — the
// useful artifact is which signals fired and how much each one mattered.
type Trace struct {
	// Terms is every contributing question, ordered by descending absolute
	// contribution so the reader sees what drove the result first.
	Terms []Term

	// Total is the composed value.
	Total float64

	// WeightSum is the sum of the weights applied.
	WeightSum float64

	// Normalized reports whether Total was divided by WeightSum.
	Normalized bool
}

// Top returns the n terms that contributed most, or all of them if there are
// fewer.
func (t Trace) Top(n int) []Term {
	if n >= len(t.Terms) {
		return t.Terms
	}
	return t.Terms[:n]
}

// String renders the trace as a short human-readable table, suitable for a log
// line or a debugging session.
func (t Trace) String() string {
	if len(t.Terms) == 0 {
		return fmt.Sprintf("total %.4f (no terms)", t.Total)
	}
	s := fmt.Sprintf("total %.4f", t.Total)
	if t.Normalized {
		s += fmt.Sprintf(" (normalized over weights summing to %.4f)", t.WeightSum)
	}
	for _, term := range t.Terms {
		s += fmt.Sprintf("\n  %-28s %.4f x %.4f = %.4f",
			term.QuestionID, term.Weight, term.Value, term.Contribution)
	}
	return s
}

// sortTerms orders terms by descending absolute contribution, breaking ties by
// question id so the output is deterministic across runs.
func sortTerms(terms []Term) {
	sort.Slice(terms, func(i, j int) bool {
		a, b := abs(terms[i].Contribution), abs(terms[j].Contribution)
		if a != b {
			return a > b
		}
		return terms[i].QuestionID < terms[j].QuestionID
	})
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
