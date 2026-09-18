package decision

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Verdict is what a Policy decided.
type Verdict int

// Verdicts, ordered from most permissive to most restrictive so they compare
// meaningfully: v >= decision.Review is a sensible test.
const (
	// Allow means proceed with no further action.
	Allow Verdict = iota

	// Warn means proceed, but flag it.
	Warn

	// Review means hold for a person.
	Review

	// Block means refuse.
	Block
)

func (v Verdict) String() string {
	switch v {
	case Allow:
		return "allow"
	case Warn:
		return "warn"
	case Review:
		return "review"
	case Block:
		return "block"
	default:
		return "unknown"
	}
}

// MarshalJSON renders the verdict as its name, so a policy decision written to
// a log or an audit record is readable without a lookup table.
func (v Verdict) MarshalJSON() ([]byte, error) { return json.Marshal(v.String()) }

// UnmarshalJSON parses a verdict name.
func (v *Verdict) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	switch s {
	case "allow":
		*v = Allow
	case "warn":
		*v = Warn
	case "review":
		*v = Review
	case "block":
		*v = Block
	default:
		return fmt.Errorf("decision: unknown verdict %q", s)
	}
	return nil
}

// Policy is a named, reusable, serializable decision rule.
//
//	var SpamPolicy = decision.Policy{
//	    Name: "spam-v3",
//	    Weights: decision.Weights{
//	        "asks_for_credentials":  0.4,
//	        "creates_time_pressure": 0.3,
//	        "generic_greeting":      0.1,
//	    },
//	    WarnAbove:   0.4,
//	    ReviewAbove: 0.6,
//	    BlockAbove:  0.9,
//	}
//
//	result, err := SpamPolicy.Evaluate(resp)
//	switch result.Verdict {
//	case decision.Block: quarantine()
//	case decision.Review: queueForHuman()
//	}
//
// # Why it marshals
//
// A Policy is data, not code. It round-trips through JSON, which means it can
// live in configuration, be versioned alongside your service, diffed in
// review, and reloaded without a deploy. Thresholds are exactly the thing
// teams tune most often and want least to rebuild for.
//
// It also means a decision can be reproduced: log the policy name and the
// answers, and you can replay the verdict months later.
type Policy struct {
	// Name identifies the policy in logs and audit records. Version it.
	Name string `json:"name,omitempty"`

	// Weights are the per-question coefficients. Every named question must be
	// a Noul.
	Weights Weights `json:"weights"`

	// Normalize divides the weighted sum by the total weight, keeping the
	// score in [0,1] so thresholds stay stable when a signal is added.
	// Defaults to true via NewPolicy; the zero value does not normalize.
	Normalize bool `json:"normalize"`

	// Thresholds, applied from the most restrictive down. A score at or above
	// BlockAbove blocks, otherwise at or above ReviewAbove reviews, and so on.
	// Leave one at zero to disable that verdict.
	WarnAbove   float64 `json:"warn_above,omitempty"`
	ReviewAbove float64 `json:"review_above,omitempty"`
	BlockAbove  float64 `json:"block_above,omitempty"`

	// OnMissing says what to do when a weighted question has no answer.
	// Defaults to failing, so a policy drifting out of step with the request
	// that feeds it is loud rather than quietly wrong.
	OnMissing MissingPolicy `json:"-"`
}

// NewPolicy returns a normalized policy with the given weights.
func NewPolicy(name string, w Weights) Policy {
	return Policy{Name: name, Weights: w, Normalize: true}
}

// Result is a policy evaluation, with the reasoning attached.
type Result struct {
	// Policy is the name of the policy that produced this.
	Policy string `json:"policy,omitempty"`

	// Verdict is the decision.
	Verdict Verdict `json:"verdict"`

	// Score is the composed value the verdict was derived from.
	Score float64 `json:"score"`

	// Trace is the full accounting: which questions contributed what.
	Trace Trace `json:"-"`
}

// String renders the result and its top contributors, for a log line.
func (r Result) String() string {
	s := fmt.Sprintf("%s: %s (score %.4f)", r.Policy, r.Verdict, r.Score)
	for _, t := range r.Trace.Top(3) {
		s += fmt.Sprintf("\n  %s %.4f x %.4f = %.4f",
			t.QuestionID, t.Weight, t.Value, t.Contribution)
	}
	return s
}

// Validate reports whether the thresholds are ordered sensibly.
//
// Worth calling at startup on any policy loaded from configuration: a
// ReviewAbove greater than BlockAbove makes the Review verdict unreachable,
// and nothing at runtime would tell you.
func (p Policy) Validate() error {
	if len(p.Weights) == 0 {
		return fmt.Errorf("decision: policy %q has no weights", p.Name)
	}
	for id, w := range p.Weights {
		if w < 0 {
			return fmt.Errorf("decision: policy %q: negative weight %v for %q", p.Name, w, id)
		}
	}

	type bound struct {
		name string
		val  float64
	}
	// Only compare thresholds that are actually enabled.
	var active []bound
	for _, b := range []bound{
		{"WarnAbove", p.WarnAbove},
		{"ReviewAbove", p.ReviewAbove},
		{"BlockAbove", p.BlockAbove},
	} {
		if b.val > 0 {
			active = append(active, b)
		}
	}
	for i := 1; i < len(active); i++ {
		if active[i].val < active[i-1].val {
			return fmt.Errorf(
				"decision: policy %q: %s (%.3f) is below %s (%.3f), making %s unreachable",
				p.Name, active[i].name, active[i].val,
				active[i-1].name, active[i-1].val, active[i].name)
		}
	}
	return nil
}

// Evaluate composes the answers and returns the verdict with its reasoning.
func (p Policy) Evaluate(src Source) (Result, error) {
	w := WeightedNoul{weights: p.Weights, normalize: p.Normalize, missing: p.OnMissing}
	trace, err := w.Explain(src)
	if err != nil {
		return Result{Policy: p.Name}, err
	}
	return Result{
		Policy:  p.Name,
		Verdict: p.verdict(trace.Total),
		Score:   trace.Total,
		Trace:   trace,
	}, nil
}

// verdict applies the thresholds, most restrictive first.
func (p Policy) verdict(score float64) Verdict {
	switch {
	case p.BlockAbove > 0 && score >= p.BlockAbove:
		return Block
	case p.ReviewAbove > 0 && score >= p.ReviewAbove:
		return Review
	case p.WarnAbove > 0 && score >= p.WarnAbove:
		return Warn
	default:
		return Allow
	}
}

// Questions returns the question ids this policy reads, sorted.
//
// Use it to build the request from the policy, so the two cannot drift:
//
//	qs := make(map[string]typesafe.Question, len(SpamPolicy.Questions()))
//	for _, id := range SpamPolicy.Questions() {
//	    qs[id] = signals[id]
//	}
func (p Policy) Questions() []string {
	out := make([]string, 0, len(p.Weights))
	for id := range p.Weights {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// LoadPolicy parses a policy from JSON and validates it.
func LoadPolicy(b []byte) (Policy, error) {
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return Policy{}, fmt.Errorf("decision: parsing policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	return p, nil
}
