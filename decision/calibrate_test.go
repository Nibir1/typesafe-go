package decision_test

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/decision"
)

// syntheticHistory generates labeled examples from known weights, so a fit can
// be checked against the truth rather than against itself.
func syntheticHistory(n int, trueWeights map[string]float64, noise float64, seed uint64) []decision.Example {
	rng := rand.New(rand.NewPCG(seed, seed*7+1))
	ids := make([]string, 0, len(trueWeights))
	for id := range trueWeights {
		ids = append(ids, id)
	}
	// Deterministic order.
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}

	out := make([]decision.Example, 0, n)
	for range n {
		answers := make(map[string]float64, len(ids))
		var score float64
		for _, id := range ids {
			v := rng.Float64()
			answers[id] = v
			score += trueWeights[id] * v
		}
		// Label positive when the true composite clears the midpoint, with
		// some label noise so the problem is not trivially separable.
		var total float64
		for _, w := range trueWeights {
			total += w
		}
		p := score / total
		label := p > 0.5
		if rng.Float64() < noise {
			label = !label
		}
		out = append(out, decision.Example{Answers: answers, Label: label})
	}
	return out
}

// TestCalibrateRecoversKnownWeights is the test that decides whether this is
// worth shipping. Generate data from weights we chose, fit, and check the
// fitted ordering matches.
func TestCalibrateRecoversKnownWeights(t *testing.T) {
	truth := map[string]float64{
		"strong_signal": 0.60,
		"medium_signal": 0.30,
		"weak_signal":   0.10,
	}
	history := syntheticHistory(1200, truth, 0.03, 42)

	report, err := decision.Calibrate(history,
		[]string{"strong_signal", "medium_signal", "weak_signal"},
		decision.CalibrationOptions{})
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	if !report.Separable {
		t.Fatalf("a fit on clean synthetic data should be separable:\n%s", report)
	}
	if report.HoldoutAccuracy < 0.85 {
		t.Errorf("holdout accuracy %.3f is too low for this data:\n%s", report.HoldoutAccuracy, report)
	}

	// The ordering is what matters: a caller uses these as relative weights.
	w := report.Weights
	if !(w["strong_signal"] > w["medium_signal"] && w["medium_signal"] > w["weak_signal"]) {
		t.Errorf("fitted weights do not preserve the true ordering:\n%s", report)
	}
	// And the dominant signal should be recognizably dominant.
	if w["strong_signal"] < 0.4 {
		t.Errorf("strong_signal fitted to %.3f, expected it to dominate:\n%s", w["strong_signal"], report)
	}

	var sum float64
	for _, v := range w {
		sum += v
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("weights sum to %v, want 1 so they drop into a Policy directly", sum)
	}
}

// TestCalibrateReportsAnUnlearnableProblem is the guard against the failure
// that matters most: a fit on noise still returns weights, and those weights
// still produce confident verdicts.
func TestCalibrateReportsAnUnlearnableProblem(t *testing.T) {
	rng := rand.New(rand.NewPCG(99, 100))
	history := make([]decision.Example, 0, 600)
	for range 600 {
		history = append(history, decision.Example{
			Answers: map[string]float64{"a": rng.Float64(), "b": rng.Float64()},
			Label:   rng.Float64() < 0.5, // independent of the answers
		})
	}

	report, err := decision.Calibrate(history, []string{"a", "b"}, decision.CalibrationOptions{})
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	if report.Separable {
		t.Errorf("random labels were reported as separable:\n%s", report)
	}
	if !strings.Contains(report.String(), "WARNING") {
		t.Errorf("the report should warn loudly:\n%s", report)
	}
}

// TestCalibrateRefusesTooLittleData: weights fitted to a dozen examples
// describe the sample. The caller should learn that here, not in production.
func TestCalibrateRefusesTooLittleData(t *testing.T) {
	history := syntheticHistory(10, map[string]float64{"a": 1}, 0, 1)
	_, err := decision.Calibrate(history, []string{"a"}, decision.CalibrationOptions{})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "minimum") {
		t.Errorf("err = %v, want it to explain the floor", err)
	}

	// The floor is configurable for callers who know what they are doing.
	if _, err := decision.Calibrate(history, []string{"a"},
		decision.CalibrationOptions{MinExamples: 5}); err != nil {
		t.Errorf("an explicit lower floor should be honored: %v", err)
	}
}

func TestCalibrateInputValidation(t *testing.T) {
	good := syntheticHistory(100, map[string]float64{"a": 1, "b": 1}, 0.05, 3)

	t.Run("no questions", func(t *testing.T) {
		if _, err := decision.Calibrate(good, nil, decision.CalibrationOptions{}); err == nil {
			t.Error("expected an error")
		}
	})

	t.Run("missing answer fails rather than imputing", func(t *testing.T) {
		_, err := decision.Calibrate(good, []string{"a", "absent"}, decision.CalibrationOptions{})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "absent") {
			t.Errorf("err = %v, want it to name the missing question", err)
		}
	})

	t.Run("single-class data", func(t *testing.T) {
		all := make([]decision.Example, 0, 50)
		for range 50 {
			all = append(all, decision.Example{Answers: map[string]float64{"a": 0.5}, Label: true})
		}
		_, err := decision.Calibrate(all, []string{"a"}, decision.CalibrationOptions{})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "same label") {
			t.Errorf("err = %v, want it to explain why", err)
		}
	})
}

// TestCalibrateIsDeterministic: a calibration nobody can reproduce is not
// evidence of anything.
func TestCalibrateIsDeterministic(t *testing.T) {
	history := syntheticHistory(300, map[string]float64{"a": 0.7, "b": 0.3}, 0.05, 11)

	first, err := decision.Calibrate(history, []string{"a", "b"}, decision.CalibrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		got, err := decision.Calibrate(history, []string{"a", "b"}, decision.CalibrationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Weights["a"] != first.Weights["a"] || got.Weights["b"] != first.Weights["b"] {
			t.Fatal("calibration is not reproducible")
		}
		if got.SuggestedThreshold != first.SuggestedThreshold {
			t.Fatal("suggested threshold is not reproducible")
		}
	}

	// Question order must not matter either.
	swapped, err := decision.Calibrate(history, []string{"b", "a"}, decision.CalibrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(swapped.Weights["a"]-first.Weights["a"]) > 1e-12 {
		t.Error("reordering the question list changed the fit")
	}
}

// TestNegativeCoefficientsAreNotSilentlyFlipped: a signal arguing against the
// positive class cannot be expressed in a Policy's weighted sum, and negating
// it would invert its meaning. It must be dropped and stay visible in Raw.
func TestNegativeCoefficientsAreNotSilentlyFlipped(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	history := make([]decision.Example, 0, 500)
	for range 500 {
		good := rng.Float64() // argues against
		bad := rng.Float64()  // argues for
		history = append(history, decision.Example{
			Answers: map[string]float64{"reassuring": good, "alarming": bad},
			Label:   bad-good > 0,
		})
	}

	report, err := decision.Calibrate(history, []string{"reassuring", "alarming"},
		decision.CalibrationOptions{})
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	if report.Raw["reassuring"] >= 0 {
		t.Errorf("expected a negative raw coefficient for the protective signal:\n%s", report)
	}
	if _, present := report.Weights["reassuring"]; present {
		t.Errorf("a negative coefficient must not appear in Policy weights:\n%s", report)
	}
	if report.Weights["alarming"] == 0 {
		t.Errorf("the positive signal should carry the weight:\n%s", report)
	}
}

// TestCalibratedWeightsDriveAPolicy closes the loop: the output is directly
// usable, which is the whole point.
func TestCalibratedWeightsDriveAPolicy(t *testing.T) {
	truth := map[string]float64{"signal_a": 0.7, "signal_b": 0.3}
	report, err := decision.Calibrate(syntheticHistory(800, truth, 0.03, 77),
		[]string{"signal_a", "signal_b"}, decision.CalibrationOptions{})
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	policy := decision.NewPolicy("fitted-v1", report.Weights)
	policy.BlockAbove = report.SuggestedThreshold
	if err := policy.Validate(); err != nil {
		t.Fatalf("a fitted policy should validate: %v", err)
	}

	hot, err := policy.Evaluate(decision.Nouls{"signal_a": 0.95, "signal_b": 0.9})
	if err != nil {
		t.Fatal(err)
	}
	cold, err := policy.Evaluate(decision.Nouls{"signal_a": 0.05, "signal_b": 0.1})
	if err != nil {
		t.Fatal(err)
	}
	if hot.Score <= cold.Score {
		t.Errorf("fitted policy does not separate: hot %.3f vs cold %.3f", hot.Score, cold.Score)
	}
	if hot.Verdict != decision.Block {
		t.Errorf("a strongly positive case scored %.3f and got %s", hot.Score, hot.Verdict)
	}
}
