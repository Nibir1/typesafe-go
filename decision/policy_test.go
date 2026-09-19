package decision_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/decision"
)

// spamPolicy is the worked example from TypeSafe's composite-scoring guidance:
// a broad judgment decomposed into atomic signals, combined with weights the
// caller owns.
var spamPolicy = decision.Policy{
	Name: "spam-v1",
	Weights: decision.Weights{
		"asks_for_credentials":  0.4,
		"creates_time_pressure": 0.3,
		"generic_greeting":      0.1,
		"has_suspicious_link":   0.2,
	},
	Normalize:   true,
	WarnAbove:   0.3,
	ReviewAbove: 0.5,
	BlockAbove:  0.8,
}

func TestPolicyEvaluate(t *testing.T) {
	cases := []struct {
		name    string
		answers decision.Nouls
		want    decision.Verdict
	}{
		{"clean", decision.Nouls{
			"asks_for_credentials": 0.01, "creates_time_pressure": 0.02,
			"generic_greeting": 0.05, "has_suspicious_link": 0.01,
		}, decision.Allow},
		{"mildly suspicious", decision.Nouls{
			"asks_for_credentials": 0.2, "creates_time_pressure": 0.5,
			"generic_greeting": 0.9, "has_suspicious_link": 0.3,
		}, decision.Warn},
		{"probably spam", decision.Nouls{
			"asks_for_credentials": 0.7, "creates_time_pressure": 0.6,
			"generic_greeting": 0.8, "has_suspicious_link": 0.4,
		}, decision.Review},
		{"certainly spam", decision.Nouls{
			"asks_for_credentials": 0.98, "creates_time_pressure": 0.95,
			"generic_greeting": 0.9, "has_suspicious_link": 0.99,
		}, decision.Block},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := spamPolicy.Evaluate(tc.answers)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got.Verdict != tc.want {
				t.Errorf("verdict = %s (score %.4f), want %s\n%s",
					got.Verdict, got.Score, tc.want, got.Trace)
			}
			if got.Score < 0 || got.Score > 1 {
				t.Errorf("normalized score %v is outside [0,1]", got.Score)
			}
			if got.Policy != "spam-v1" {
				t.Errorf("Policy = %q", got.Policy)
			}
		})
	}
}

// TestNormalizationKeepsThresholdsStable is the reason Normalize exists.
// Adding a signal must not silently move every threshold.
func TestNormalizationKeepsThresholdsStable(t *testing.T) {
	base := decision.NewPolicy("base", decision.Weights{"a": 1, "b": 1})
	base.ReviewAbove = 0.5

	grown := decision.NewPolicy("grown", decision.Weights{"a": 1, "b": 1, "c": 1})
	grown.ReviewAbove = 0.5

	// Every signal at 0.6: the composite should stay 0.6 regardless of how
	// many signals there are.
	r1, err := base.Evaluate(decision.Nouls{"a": 0.6, "b": 0.6})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := grown.Evaluate(decision.Nouls{"a": 0.6, "b": 0.6, "c": 0.6})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r1.Score-r2.Score) > 1e-9 {
		t.Errorf("adding a signal moved the score: %.4f -> %.4f", r1.Score, r2.Score)
	}
	if r1.Verdict != r2.Verdict {
		t.Errorf("adding a signal changed the verdict: %s -> %s", r1.Verdict, r2.Verdict)
	}

	// Unnormalized, the same change doubles the total, which is exactly the
	// failure mode normalization prevents.
	raw := decision.NewWeightedNoul(decision.Weights{"a": 1, "b": 1}).Unnormalized()
	v, _ := raw.Apply(decision.Nouls{"a": 0.6, "b": 0.6})
	if math.Abs(v-1.2) > 1e-9 {
		t.Errorf("unnormalized sum = %v, want 1.2", v)
	}
}

// TestMissingAnswerFailsLoudly: a weight naming a question nobody asked is a
// policy that has drifted from its request. Scoring it as zero would turn that
// mistake into a plausible number.
func TestMissingAnswerFailsLoudly(t *testing.T) {
	_, err := spamPolicy.Evaluate(decision.Nouls{"asks_for_credentials": 0.9})
	if err == nil {
		t.Fatal("expected an error for missing answers")
	}
	if !errors.Is(err, decision.ErrMissingAnswer) {
		t.Errorf("err = %v, want ErrMissingAnswer", err)
	}
	if !strings.Contains(err.Error(), "creates_time_pressure") {
		t.Errorf("the error should name a missing question, got: %v", err)
	}
}

func TestMissingPolicies(t *testing.T) {
	w := decision.NewWeightedNoul(decision.Weights{"a": 1, "b": 1})
	present := decision.Nouls{"a": 0.8}

	t.Run("zero counts against the score", func(t *testing.T) {
		got, err := w.OnMissing(decision.MissingIsZero).Apply(present)
		if err != nil {
			t.Fatal(err)
		}
		// (1*0.8 + 1*0) / 2
		if math.Abs(got-0.4) > 1e-9 {
			t.Errorf("= %v, want 0.4", got)
		}
	})

	t.Run("skipped scores the rest among themselves", func(t *testing.T) {
		got, err := w.OnMissing(decision.MissingIsSkipped).Apply(present)
		if err != nil {
			t.Fatal(err)
		}
		// (1*0.8) / 1
		if math.Abs(got-0.8) > 1e-9 {
			t.Errorf("= %v, want 0.8", got)
		}
	})

	t.Run("default substitutes a value", func(t *testing.T) {
		got, err := w.WithDefault(0.5).Apply(present)
		if err != nil {
			t.Fatal(err)
		}
		// (1*0.8 + 1*0.5) / 2
		if math.Abs(got-0.65) > 1e-9 {
			t.Errorf("= %v, want 0.65", got)
		}
	})
}

// TestExplainIsDeterministic: a trace that reorders between runs cannot be
// diffed in an audit log.
func TestExplainIsDeterministic(t *testing.T) {
	answers := decision.Nouls{
		"asks_for_credentials": 0.7, "creates_time_pressure": 0.6,
		"generic_greeting": 0.8, "has_suspicious_link": 0.4,
	}
	first, err := spamPolicy.Evaluate(answers)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		got, err := spamPolicy.Evaluate(answers)
		if err != nil {
			t.Fatal(err)
		}
		if got.Score != first.Score || got.Verdict != first.Verdict {
			t.Fatalf("run %d differed", i)
		}
		if len(got.Trace.Terms) != len(first.Trace.Terms) {
			t.Fatalf("run %d has %d terms, want %d", i, len(got.Trace.Terms), len(first.Trace.Terms))
		}
		for j := range got.Trace.Terms {
			if got.Trace.Terms[j] != first.Trace.Terms[j] {
				t.Fatalf("run %d term %d = %+v, want %+v", i, j, got.Trace.Terms[j], first.Trace.Terms[j])
			}
		}
	}
}

// TestTraceRanksByContribution: the reader wants to see what drove the score.
func TestTraceRanksByContribution(t *testing.T) {
	got, err := spamPolicy.Evaluate(decision.Nouls{
		"asks_for_credentials":  0.9, // 0.4 * 0.9 = 0.36, highest
		"creates_time_pressure": 0.1, // 0.03
		"generic_greeting":      0.5, // 0.05
		"has_suspicious_link":   0.8, // 0.16, second
	})
	if err != nil {
		t.Fatal(err)
	}
	terms := got.Trace.Terms
	if terms[0].QuestionID != "asks_for_credentials" {
		t.Errorf("top term = %q, want asks_for_credentials", terms[0].QuestionID)
	}
	if terms[1].QuestionID != "has_suspicious_link" {
		t.Errorf("second term = %q, want has_suspicious_link", terms[1].QuestionID)
	}
	for i := 1; i < len(terms); i++ {
		if terms[i].Contribution > terms[i-1].Contribution+1e-12 {
			t.Errorf("terms not ordered by contribution at %d", i)
		}
	}
	if top := got.Trace.Top(2); len(top) != 2 {
		t.Errorf("Top(2) returned %d terms", len(top))
	}
	if all := got.Trace.Top(99); len(all) != len(terms) {
		t.Errorf("Top(99) should return everything, got %d", len(all))
	}

	// The rendered form has to name the question and the numbers.
	s := got.Trace.String()
	for _, want := range []string{"asks_for_credentials", "total"} {
		if !strings.Contains(s, want) {
			t.Errorf("trace output missing %q:\n%s", want, s)
		}
	}
}

// --- policy as data ----------------------------------------------------------

func TestPolicyRoundTripsThroughJSON(t *testing.T) {
	b, err := json.Marshal(spamPolicy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := decision.LoadPolicy(b)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if back.Name != spamPolicy.Name || len(back.Weights) != len(spamPolicy.Weights) {
		t.Errorf("round trip lost data: %+v", back)
	}

	answers := decision.Nouls{
		"asks_for_credentials": 0.7, "creates_time_pressure": 0.6,
		"generic_greeting": 0.8, "has_suspicious_link": 0.4,
	}
	a, _ := spamPolicy.Evaluate(answers)
	c, _ := back.Evaluate(answers)
	if a.Score != c.Score || a.Verdict != c.Verdict {
		t.Errorf("a reloaded policy decided differently: %v/%v vs %v/%v",
			a.Verdict, a.Score, c.Verdict, c.Score)
	}
}

func TestVerdictJSON(t *testing.T) {
	for _, v := range []decision.Verdict{decision.Allow, decision.Warn, decision.Review, decision.Block} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %v: %v", v, err)
		}
		if string(b) != `"`+v.String()+`"` {
			t.Errorf("marshal(%v) = %s, want a readable name", v, b)
		}
		var back decision.Verdict
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if back != v {
			t.Errorf("round trip: %v -> %v", v, back)
		}
	}
	var v decision.Verdict
	if err := json.Unmarshal([]byte(`"nonsense"`), &v); err == nil {
		t.Error("an unknown verdict name should fail to parse")
	}
}

// TestPolicyValidationCatchesUnreachableVerdicts: thresholds out of order make
// a verdict impossible, and nothing at runtime would say so.
func TestPolicyValidation(t *testing.T) {
	t.Run("ordered thresholds pass", func(t *testing.T) {
		if err := spamPolicy.Validate(); err != nil {
			t.Errorf("Validate: %v", err)
		}
	})

	t.Run("review above block is unreachable", func(t *testing.T) {
		p := spamPolicy
		p.ReviewAbove = 0.9
		p.BlockAbove = 0.5
		err := p.Validate()
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "unreachable") {
			t.Errorf("err = %v, want it to explain the consequence", err)
		}
	})

	t.Run("no weights", func(t *testing.T) {
		if err := (decision.Policy{Name: "empty"}).Validate(); err == nil {
			t.Error("a policy with no weights should fail validation")
		}
	})

	t.Run("negative weight", func(t *testing.T) {
		p := decision.NewPolicy("neg", decision.Weights{"a": -1})
		if err := p.Validate(); err == nil {
			t.Error("a negative weight should fail validation")
		}
	})

	t.Run("LoadPolicy validates", func(t *testing.T) {
		_, err := decision.LoadPolicy([]byte(`{"name":"x","weights":{"a":1},"review_above":0.9,"block_above":0.1}`))
		if err == nil {
			t.Error("LoadPolicy should reject unreachable thresholds")
		}
		if _, err := decision.LoadPolicy([]byte(`{{`)); err == nil {
			t.Error("LoadPolicy should reject malformed JSON")
		}
	})
}

// TestQuestionsKeepsThePolicyAndRequestInStep.
func TestPolicyQuestions(t *testing.T) {
	got := spamPolicy.Questions()
	want := []string{"asks_for_credentials", "creates_time_pressure", "generic_greeting", "has_suspicious_link"}
	if len(got) != len(want) {
		t.Fatalf("Questions() = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Questions()[%d] = %q, want %q (sorted)", i, got[i], want[i])
		}
	}
}

func TestVerdictOrdering(t *testing.T) {
	// Verdicts must compare meaningfully: v >= Review is a sensible test.
	if decision.Allow > decision.Warn || decision.Warn > decision.Review ||
		decision.Review > decision.Block {
		t.Error("verdicts are not ordered from permissive to restrictive")
	}
}

func TestResultString(t *testing.T) {
	got, _ := spamPolicy.Evaluate(decision.Nouls{
		"asks_for_credentials": 0.9, "creates_time_pressure": 0.9,
		"generic_greeting": 0.9, "has_suspicious_link": 0.9,
	})
	s := got.String()
	for _, want := range []string{"spam-v1", "block", "asks_for_credentials"} {
		if !strings.Contains(s, want) {
			t.Errorf("Result.String() missing %q:\n%s", want, s)
		}
	}
}

func TestNoulsSourceRejectsWrongPrimitives(t *testing.T) {
	n := decision.Nouls{"a": 0.5}
	if _, err := n.Choice("a"); !errors.Is(err, decision.ErrWrongType) {
		t.Errorf("Choice on a Nouls map: err = %v, want ErrWrongType", err)
	}
	if _, err := n.Score("a"); !errors.Is(err, decision.ErrWrongType) {
		t.Errorf("Score on a Nouls map: err = %v, want ErrWrongType", err)
	}
}

// TestPolicyWorksAgainstARealResponse: the Source interface has to fit the
// actual response type, not just the test double.
func TestPolicyAgainstARealResponse(t *testing.T) {
	var resp typesafe.SystemOneResponse
	body := `{"model":"jev-1.13.0","answers":{
	  "asks_for_credentials":{"type":"noul","noul":0.95},
	  "creates_time_pressure":{"type":"noul","noul":0.9},
	  "generic_greeting":{"type":"noul","noul":0.85},
	  "has_suspicious_link":{"type":"noul","noul":0.99}},
	  "usage":{"input_tokens":1,"output_tokens":1}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	got, err := spamPolicy.Evaluate(&resp)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Verdict != decision.Block {
		t.Errorf("verdict = %s, want block\n%s", got.Verdict, got.Trace)
	}
}
