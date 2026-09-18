package typesafe

import (
	"encoding/json"
	"sort"
)

// Choice selects exactly one option from a set you define.
//
//	typesafe.Choice{
//	    Instructions: "Which team should handle this?",
//	    Criteria: typesafe.Options{
//	        "billing":   "Payments, invoicing, refunds",
//	        "technical": "Bugs, outages, integrations",
//	        "sales":     nil, // interpreted by its name alone
//	    },
//	}
//
// The answer names the winning option and gives a probability for every one,
// so the result is always a member of the set you supplied. Include a catch-all
// option when your set may not cover every input — without one, the model must
// pick from what it was given.
type Choice struct {
	// Instructions is what the model should decide. Optional per the schema.
	Instructions EntryType

	// Criteria maps each option to a description of when it applies.
	// Required, and must be non-empty.
	//
	// A nil value means "interpret this option by its name alone" and is sent
	// as JSON null.
	Criteria Options
}

// Options maps an option name to its description. A nil value is sent as JSON
// null, meaning the option is interpreted by its name alone.
type Options map[string]EntryType

func (Choice) isQuestion() {}

// MarshalJSON emits the wire form, adding the required type discriminator.
//
// Criteria is always emitted, even when empty, because the API requires the
// field to be present; Validate rejects an empty one before it is sent.
func (c Choice) MarshalJSON() ([]byte, error) {
	criteria := c.Criteria
	if criteria == nil {
		criteria = Options{}
	}
	return json.Marshal(struct {
		Type         string    `json:"type"`
		Instructions EntryType `json:"instructions,omitempty"`
		Criteria     Options   `json:"criteria"`
	}{TypeChoice, c.Instructions, criteria})
}

// ChoiceAnswer is the answer to a Choice.
type ChoiceAnswer struct {
	// Choice is the highest-probability option.
	Choice string `json:"choice"`

	// Probabilities gives every option's probability. They sum to 1.
	Probabilities map[string]float64 `json:"probabilities"`

	// Confidence in [0,1], derived from the shape of the distribution. A flat
	// distribution means no option clearly won, which usually means the
	// options overlap or the state does not contain enough to decide.
	Confidence float64 `json:"confidence"`
}

func (ChoiceAnswer) isAnswer()    {}
func (ChoiceAnswer) Type() string { return TypeChoice }

// Ranked returns every option ordered by descending probability.
//
// Ties break by option name so the order is deterministic across runs and
// machines — Go map iteration is not, and a non-deterministic ranking would
// make snapshot tests and audit logs unstable.
func (a ChoiceAnswer) Ranked() []RankedOption {
	out := make([]RankedOption, 0, len(a.Probabilities))
	for opt, p := range a.Probabilities {
		out = append(out, RankedOption{Option: opt, Probability: p})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Probability != out[j].Probability {
			return out[i].Probability > out[j].Probability
		}
		return out[i].Option < out[j].Option
	})
	return out
}

// RankedOption is one option and its probability.
type RankedOption struct {
	Option      string
	Probability float64
}

// Margin is the gap between the top two options.
//
// It answers a different question than Confidence: a wide margin means the
// winner beat its nearest rival clearly, even if probability is spread across
// the remaining options. Returns the winner's probability when there is only
// one option.
func (a ChoiceAnswer) Margin() float64 {
	r := a.Ranked()
	switch len(r) {
	case 0:
		return 0
	case 1:
		return r[0].Probability
	default:
		return r[0].Probability - r[1].Probability
	}
}

// ProbabilityOf returns the probability assigned to an option, and whether the
// option was part of the answer at all.
func (a ChoiceAnswer) ProbabilityOf(option string) (float64, bool) {
	p, ok := a.Probabilities[option]
	return p, ok
}
