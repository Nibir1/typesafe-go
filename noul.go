package typesafe

import "encoding/json"

// Noul is a yes/no question, answered with the probability that the answer is
// yes.
//
//	typesafe.Noul{Instructions: "Does this convey urgency?"}
//
// The answer is a single number in [0,1]. Near 1 is a strong yes, near 0 a
// strong no, and near 0.5 is the model saying it does not know — which is why
// a Noul answer carries no separate confidence field. The probability is the
// uncertainty.
//
// Both fields are optional per the API schema, though a Noul with neither says
// nothing about what to judge; Validate warns about that.
type Noul struct {
	// Instructions is the yes/no question or statement to evaluate.
	//
	// Phrase it positively. The model reads instructions literally, and a
	// double negative costs accuracy — see the "Indirection" entry in
	// TypeSafe's published model-jaggedness notes.
	Instructions EntryType

	// Criteria optionally describes what a yes and a no mean. Nil is omitted.
	//
	// Keep it aligned with Instructions. A Noul whose true describes a "no"
	// performs measurably worse than one phrased consistently.
	//
	// A nil True or False is omitted from the request rather than sent as an
	// explicit null. The schema marks both optional and nullable, so the two
	// encodings are equivalent to the server and this SDK emits the shorter
	// one. Contrast Choice, where a nil option description must be sent as
	// null — there the key itself carries the option name, so omitting it
	// would remove the option.
	Criteria *NoulCriteria
}

// NoulCriteria describes what each end of the 0-to-1 range means.
type NoulCriteria struct {
	// True is what a value near 1 means.
	True EntryType `json:"true,omitempty"`

	// False is what a value near 0 means.
	False EntryType `json:"false,omitempty"`
}

func (Noul) isQuestion() {}

// MarshalJSON emits the wire form, adding the required type discriminator.
func (n Noul) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type         string        `json:"type"`
		Instructions EntryType     `json:"instructions,omitempty"`
		Criteria     *NoulCriteria `json:"criteria,omitempty"`
	}{TypeNoul, n.Instructions, n.Criteria})
}

// NoulAnswer is the answer to a Noul.
//
// It has no Confidence field, and that is deliberate on the API's part rather
// than an omission here: the probability already expresses the uncertainty.
// Code that reaches for a confidence on a Noul is reading a zero that means
// nothing.
type NoulAnswer struct {
	// Noul is the probability that the answer is yes, in [0,1].
	Noul float64 `json:"noul"`
}

func (NoulAnswer) isAnswer()    {}
func (NoulAnswer) Type() string { return TypeNoul }

// Bool collapses the probability to a decision at the given threshold.
//
// The threshold is your policy, not the model's: there is no universally
// correct value, and the right one depends on the cost of each kind of mistake
// in your domain. Name the constant you pass rather than inlining a literal.
//
//	if ans.Bool(spamThreshold) { ... }
func (a NoulAnswer) Bool(threshold float64) bool { return a.Noul >= threshold }

// Uncertain reports whether the probability sits within delta of 0.5, the
// region where the model is expressing genuine ignorance rather than a weak
// opinion.
//
//	if ans.Uncertain(0.1) { routeToHuman() }
func (a NoulAnswer) Uncertain(delta float64) bool {
	return a.Noul > 0.5-delta && a.Noul < 0.5+delta
}
