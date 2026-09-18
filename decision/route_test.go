package decision_test

import (
	"math"
	"strings"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/decision"
)

func choice(winner string, probs map[string]float64, confidence float64) typesafe.ChoiceAnswer {
	return typesafe.ChoiceAnswer{Choice: winner, Probabilities: probs, Confidence: confidence}
}

func score(levels []string, probs []float64) typesafe.ScoreAnswer {
	legend := make(map[string]any, len(levels))
	p := make(map[string]float64, len(probs))
	var weighted, best float64
	for i, l := range levels {
		legend[itoa(i)] = l
		p[itoa(i)] = probs[i]
		weighted += float64(i) * probs[i]
		if probs[i] > best {
			best = probs[i]
		}
	}
	return typesafe.ScoreAnswer{Score: weighted, Legend: legend, Probabilities: p, Confidence: best}
}

func itoa(n int) string { return string(rune('0' + n)) }

// --- thresholds --------------------------------------------------------------

func TestThreshold(t *testing.T) {
	const spam = decision.Threshold(0.85)
	if !spam.On(typesafe.NoulAnswer{Noul: 0.9}) {
		t.Error("0.9 should clear a 0.85 threshold")
	}
	if spam.On(typesafe.NoulAnswer{Noul: 0.8}) {
		t.Error("0.8 should not clear a 0.85 threshold")
	}
	if !spam.Value(0.85) {
		t.Error("the threshold should be inclusive")
	}
	if got := spam.Margin(typesafe.NoulAnswer{Noul: 0.9}); math.Abs(got-0.05) > 1e-9 {
		t.Errorf("Margin = %v, want 0.05", got)
	}
}

func TestUncertain(t *testing.T) {
	if !decision.Uncertain(0.52, 0.1) {
		t.Error("0.52 is uncertain at delta 0.1")
	}
	if decision.Uncertain(0.92, 0.1) {
		t.Error("0.92 is not uncertain")
	}
	if decision.Uncertain(0.3, 0.1) {
		t.Error("0.3 is a confident no, not uncertain")
	}
}

// --- bands -------------------------------------------------------------------

func TestBandsClassify(t *testing.T) {
	b := decision.Bands{ActAbove: 0.9, ConfirmAbove: 0.5}
	cases := map[float64]decision.Band{
		0.95: decision.Act,
		0.90: decision.Act, // inclusive
		0.70: decision.Confirm,
		0.50: decision.Confirm,
		0.49: decision.Escalate,
		0.00: decision.Escalate,
	}
	for conf, want := range cases {
		if got := b.Classify(conf); got != want {
			t.Errorf("Classify(%.2f) = %s, want %s", conf, got, want)
		}
	}
}

func TestBandsValidate(t *testing.T) {
	if err := (decision.Bands{ActAbove: 0.9, ConfirmAbove: 0.5}).Validate(); err != nil {
		t.Errorf("valid bands rejected: %v", err)
	}
	err := (decision.Bands{ActAbove: 0.5, ConfirmAbove: 0.9}).Validate()
	if err == nil {
		t.Fatal("inverted bands should fail validation")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("err = %v, want it to explain the consequence", err)
	}
}

// TestBandingANoulIsAnError is the trap this whole package has to avoid
// re-introducing. A Noul has no confidence; banding one by confidence would
// read a zero and escalate every single call.
func TestBandingANoulIsAnError(t *testing.T) {
	b := decision.Bands{ActAbove: 0.9, ConfirmAbove: 0.5}

	band, err := b.ClassifyAnswer(typesafe.NoulAnswer{Noul: 0.99})
	if err == nil {
		t.Fatal("banding a noul should be an error, not a silent Escalate")
	}
	if band != decision.Escalate {
		t.Errorf("the safe fallback should be Escalate, got %s", band)
	}
	if !strings.Contains(err.Error(), "no confidence") {
		t.Errorf("err = %v, want it to explain why", err)
	}
	// And it should show the probability, since that is what the caller wants.
	if !strings.Contains(err.Error(), "0.99") {
		t.Errorf("err = %v, want the probability surfaced", err)
	}

	// Choice and Score band normally.
	if got, err := b.ClassifyAnswer(choice("a", map[string]float64{"a": 0.95}, 0.95)); err != nil || got != decision.Act {
		t.Errorf("choice banding = %s, %v", got, err)
	}
	if got, err := b.ClassifyAnswer(score([]string{"a", "b"}, []float64{0.3, 0.7})); err != nil {
		t.Errorf("score banding failed: %v", err)
	} else if got != decision.Confirm {
		t.Errorf("score banding = %s, want confirm (confidence 0.7)", got)
	}
}

// --- routing -----------------------------------------------------------------

func TestRouter(t *testing.T) {
	r := decision.NewRouter(map[string]string{
		"billing":   "queue-billing",
		"technical": "queue-eng",
	})

	got, err := r.Route(choice("technical", map[string]float64{"billing": 0.1, "technical": 0.9}, 0.9))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got != "queue-eng" {
		t.Errorf("routed to %q", got)
	}

	// An unmapped option fails loudly rather than returning a zero value.
	_, err = r.Route(choice("sales", map[string]float64{"sales": 1}, 1))
	if err == nil {
		t.Fatal("an unmapped option should fail")
	}
	if !strings.Contains(err.Error(), "sales") || !strings.Contains(err.Error(), "billing") {
		t.Errorf("err = %v, want it to name both the option and the known set", err)
	}
}

func TestRouterFallback(t *testing.T) {
	r := decision.NewRouter(map[string]string{"billing": "queue-billing"}).
		WithFallback("queue-triage")

	got, err := r.Route(choice("unknown", map[string]float64{"unknown": 1}, 1))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got != "queue-triage" {
		t.Errorf("routed to %q, want the fallback", got)
	}
}

// TestRouterConfidenceGate: a low-confidence classification landing silently in
// a queue is the failure mode confidence exists to prevent.
func TestRouterConfidenceGate(t *testing.T) {
	r := decision.NewRouter(map[string]string{"billing": "queue-billing"}).
		RequireConfidence(0.8)

	if _, err := r.Route(choice("billing", map[string]float64{"billing": 1}, 0.5)); err == nil {
		t.Fatal("a low-confidence answer should not route")
	}

	withFallback := r.WithFallback("queue-human")
	got, err := withFallback.Route(choice("billing", map[string]float64{"billing": 1}, 0.5))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got != "queue-human" {
		t.Errorf("low confidence routed to %q, want the fallback", got)
	}

	// High confidence still routes normally.
	got, err = withFallback.Route(choice("billing", map[string]float64{"billing": 1}, 0.95))
	if err != nil || got != "queue-billing" {
		t.Errorf("high confidence routed to %q (%v)", got, err)
	}
}

// TestRouterExhaustive catches the drift between a Choice question's options
// and the router's routes — two lists nothing keeps in step.
func TestRouterExhaustive(t *testing.T) {
	r := decision.NewRouter(map[string]string{"billing": "q1", "technical": "q2"})

	full := choice("billing", map[string]float64{"billing": 0.5, "technical": 0.3, "sales": 0.2}, 0.5)
	missing := r.Exhaustive(full)
	if len(missing) != 1 || missing[0] != "sales" {
		t.Errorf("Exhaustive = %v, want [sales]", missing)
	}

	covered := choice("billing", map[string]float64{"billing": 0.7, "technical": 0.3}, 0.7)
	if got := r.Exhaustive(covered); len(got) != 0 {
		t.Errorf("Exhaustive = %v, want empty", got)
	}

	if opts := r.Options(); len(opts) != 2 || opts[0] != "billing" {
		t.Errorf("Options = %v, want sorted", opts)
	}
}

// TestRouterCarriesAnyDestination: routing to a handler is as useful as
// routing to a queue name.
func TestRouterCarriesAnyDestination(t *testing.T) {
	type handler struct{ name string }
	r := decision.NewRouter(map[string]handler{
		"billing": {"billingHandler"},
	})
	got, err := r.Route(choice("billing", map[string]float64{"billing": 1}, 1))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got.name != "billingHandler" {
		t.Errorf("got %+v", got)
	}
}

// --- score composition -------------------------------------------------------

func TestScoreThresholding(t *testing.T) {
	// Levels: calm, frustrated, angry. Mass concentrated at the top.
	s := score([]string{"calm", "frustrated", "angry"}, []float64{0.05, 0.30, 0.65})

	if got := decision.ScoreAtOrAbove(s, 1); math.Abs(got-0.95) > 1e-9 {
		t.Errorf("AtOrAbove(1) = %v, want 0.95", got)
	}
	if got := decision.ScoreAtOrAbove(s, 2); math.Abs(got-0.65) > 1e-9 {
		t.Errorf("AtOrAbove(2) = %v, want 0.65", got)
	}
	if got := decision.ScoreAtOrBelow(s, 1); math.Abs(got-0.35) > 1e-9 {
		t.Errorf("AtOrBelow(1) = %v, want 0.35", got)
	}
	if got := decision.ScoreBetween(s, 1, 1); math.Abs(got-0.30) > 1e-9 {
		t.Errorf("Between(1,1) = %v, want 0.30", got)
	}
	if got := decision.ScoreBetween(s, 2, 1); got != 0 {
		t.Errorf("Between with hi<lo = %v, want 0", got)
	}
}

func TestScoreThresholdRule(t *testing.T) {
	page := decision.ScoreThreshold{Level: 2, Probability: 0.6}
	hot := score([]string{"a", "b", "c"}, []float64{0.05, 0.30, 0.65})
	cold := score([]string{"a", "b", "c"}, []float64{0.6, 0.3, 0.1})

	if !page.On(hot) {
		t.Error("0.65 at level 2 should clear a 0.6 bar")
	}
	if page.On(cold) {
		t.Error("0.1 at level 2 should not clear a 0.6 bar")
	}
	if got := page.Margin(hot); math.Abs(got-0.05) > 1e-9 {
		t.Errorf("Margin = %v, want 0.05", got)
	}
	if page.Margin(cold) >= 0 {
		t.Error("Margin should be negative when the answer falls short")
	}
}

func TestScoreNormalized(t *testing.T) {
	s := score([]string{"a", "b", "c"}, []float64{0, 0, 1}) // score 2 of max 2
	if got := decision.ScoreNormalized(s); math.Abs(got-1) > 1e-9 {
		t.Errorf("ScoreNormalized = %v, want 1", got)
	}
	// A single-level rubric has nothing to normalize over.
	one := score([]string{"only"}, []float64{1})
	if got := decision.ScoreNormalized(one); got != 0 {
		t.Errorf("ScoreNormalized on a 1-level rubric = %v, want 0", got)
	}
}

// TestScoreIsBimodal covers the reading hazard the expected value hides: mass
// at both ends yields a mean in the middle that the model considers unlikely.
func TestScoreIsBimodal(t *testing.T) {
	split := score([]string{"harmless", "unclear", "critical"}, []float64{0.45, 0.10, 0.45})
	if !decision.ScoreIsBimodal(split, 0.3) {
		t.Error("a 0.45 / 0.10 / 0.45 distribution is bimodal")
	}
	// Its expected value sits on a level holding only 10% of the belief.
	if got := decision.ScoreExpectation(split); math.Abs(got-1) > 1e-9 {
		t.Errorf("expectation = %v, want 1 — the point of the test", got)
	}

	concentrated := score([]string{"a", "b", "c"}, []float64{0.05, 0.10, 0.85})
	if decision.ScoreIsBimodal(concentrated, 0.3) {
		t.Error("a concentrated distribution is not bimodal")
	}
	twoLevel := score([]string{"a", "b"}, []float64{0.5, 0.5})
	if decision.ScoreIsBimodal(twoLevel, 0.3) {
		t.Error("a two-level rubric has no middle to be bimodal around")
	}
}

func TestScoreConfidence(t *testing.T) {
	s := score([]string{"a", "b"}, []float64{0.2, 0.8})
	if got := decision.ScoreConfidence(s); math.Abs(got-0.8) > 1e-9 {
		t.Errorf("ScoreConfidence = %v, want 0.8", got)
	}
}
