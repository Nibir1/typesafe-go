package decision

import (
	"fmt"
	"math"
	"sort"
)

// Calibration fits policy weights from labeled history, so that thresholds
// come from data rather than from guessing.
//
// The usual way a policy is built is by picking weights that feel right,
// watching what it does, and nudging them. That works, slowly, and nobody can
// say afterwards why any particular number is what it is. If you have examples
// — tickets that turned out to be spam, incidents that turned out to be real —
// the weights can be fitted instead.
//
// # What this does and does not claim
//
// This is logistic regression by gradient descent: a small, well-understood
// model appropriate to the shape of the problem, implemented in the standard
// library with no dependencies. It is not a replacement for a real modeling
// pipeline. If you have a hundred thousand labeled examples and a data science
// team, train something better and use the output as weights here.
//
// What it is good for is the common case: a few hundred examples, a handful of
// signals, and a want for weights that are defensible rather than invented.
//
// # It reports its own fit
//
// A fit that cannot separate the classes will still return weights, and those
// weights will still produce confident-looking verdicts. Report.Separable and
// Report.Accuracy exist so that a bad fit is visible rather than silently
// shipped. Check them.

// Example is one labeled observation: the answers that were seen, and what
// turned out to be true.
type Example struct {
	// Answers maps question id to the probability that was returned.
	Answers map[string]float64

	// Label is the outcome: true for the positive class.
	Label bool

	// Weight scales this example's influence. Zero means one.
	Weight float64
}

// CalibrationOptions tunes the fit. The zero value is usable.
type CalibrationOptions struct {
	// Iterations of gradient descent. Zero uses 5000.
	Iterations int

	// LearningRate for gradient descent. Zero uses 0.1.
	LearningRate float64

	// L2 is the ridge penalty, which keeps weights from exploding when two
	// signals are nearly identical. Zero uses 0.01.
	L2 float64

	// MinExamples refuses to fit below this count. Zero uses 30.
	//
	// Fitting a handful of weights to a dozen examples produces numbers that
	// describe the sample and nothing else. The floor exists so that a user
	// discovers this at calibration time rather than in production.
	MinExamples int

	// HoldoutFraction is the share of examples reserved for evaluation.
	// Zero uses 0.25. Set to 0 via a negative value to disable.
	HoldoutFraction float64
}

func (o CalibrationOptions) withDefaults() CalibrationOptions {
	if o.Iterations <= 0 {
		o.Iterations = 5000
	}
	if o.LearningRate <= 0 {
		o.LearningRate = 0.1
	}
	if o.L2 == 0 {
		o.L2 = 0.01
	}
	if o.MinExamples <= 0 {
		o.MinExamples = 30
	}
	if o.HoldoutFraction == 0 {
		o.HoldoutFraction = 0.25
	}
	return o
}

// Report describes how well a calibration fit its data.
type Report struct {
	// Weights are the fitted coefficients, normalized to sum to 1 so they can
	// be dropped into a Policy directly.
	Weights Weights

	// Raw are the unnormalized logistic coefficients, including negative ones.
	// A negative coefficient means the signal argues *against* the positive
	// class, which Weights cannot express and which is worth seeing.
	Raw map[string]float64

	// Intercept is the fitted bias term.
	Intercept float64

	// TrainAccuracy and HoldoutAccuracy are the share correctly classified at
	// the suggested threshold.
	TrainAccuracy   float64
	HoldoutAccuracy float64

	// SuggestedThreshold is the cutoff that maximized accuracy on the holdout
	// set — a starting point for a policy's thresholds, not a final answer.
	SuggestedThreshold float64

	// Separable reports whether the fit does better than always predicting the
	// majority class, by more than two standard errors of the accuracy
	// estimate on the evaluation set.
	//
	// False means the signals do not carry the information the labels need,
	// and no choice of weights will fix that. Check it: a fit on noise still
	// returns weights, and those weights still produce confident verdicts.
	Separable bool

	// RequiredAccuracy is the bar Separable was judged against: the majority
	// class rate plus two standard errors. Exposed so a caller can see how
	// close the fit came, and how much the sample size is costing them.
	RequiredAccuracy float64

	// BaseRate is the share of positive examples.
	BaseRate float64

	// Examples and Holdout are the counts used.
	Examples int
	Holdout  int
}

// String renders the report for a terminal.
func (r Report) String() string {
	s := fmt.Sprintf("calibration over %d examples (%d held out), base rate %.1f%%\n",
		r.Examples, r.Holdout, r.BaseRate*100)
	s += fmt.Sprintf("  train accuracy   %.1f%%\n", r.TrainAccuracy*100)
	s += fmt.Sprintf("  holdout accuracy %.1f%%\n", r.HoldoutAccuracy*100)
	s += fmt.Sprintf("  threshold        %.3f\n", r.SuggestedThreshold)
	s += fmt.Sprintf("  bar to clear     %.1f%% (majority class + 2 s.e.)\n", r.RequiredAccuracy*100)
	if !r.Separable {
		s += "  WARNING: not meaningfully better than predicting the majority\n" +
			"           class. These weights describe noise. Either the signals\n" +
			"           do not predict the label, or there are too few examples\n" +
			"           to tell.\n"
	}
	ids := make([]string, 0, len(r.Raw))
	for id := range r.Raw {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return math.Abs(r.Raw[ids[i]]) > math.Abs(r.Raw[ids[j]]) })
	for _, id := range ids {
		s += fmt.Sprintf("  %-28s raw %+.4f  weight %.4f\n", id, r.Raw[id], r.Weights[id])
	}
	return s
}

// Calibrate fits weights for the given question ids from labeled examples.
//
//	report, err := decision.Calibrate(history, []string{
//	    "asks_for_credentials", "creates_time_pressure", "generic_greeting",
//	}, decision.CalibrationOptions{})
//	if err != nil {
//	    return err
//	}
//	if !report.Separable {
//	    return fmt.Errorf("signals do not predict the label:\n%s", report)
//	}
//	policy := decision.NewPolicy("spam-v4", report.Weights)
//
// Deterministic: the same examples in the same order produce the same weights,
// so a calibration can be reproduced and diffed.
func Calibrate(examples []Example, questions []string, opts CalibrationOptions) (Report, error) {
	o := opts.withDefaults()

	if len(questions) == 0 {
		return Report{}, fmt.Errorf("decision: calibrate: no questions given")
	}
	if len(examples) < o.MinExamples {
		return Report{}, fmt.Errorf(
			"decision: calibrate: %d examples is below the minimum of %d; "+
				"weights fitted to this little data describe the sample, not the problem",
			len(examples), o.MinExamples)
	}

	ids := append([]string(nil), questions...)
	sort.Strings(ids) // deterministic feature order

	// Build the design matrix, failing on any example missing a question
	// rather than imputing a value nobody chose.
	xs := make([][]float64, 0, len(examples))
	ys := make([]float64, 0, len(examples))
	ws := make([]float64, 0, len(examples))
	var positives int

	for i, ex := range examples {
		row := make([]float64, len(ids))
		for j, id := range ids {
			v, ok := ex.Answers[id]
			if !ok {
				return Report{}, fmt.Errorf(
					"decision: calibrate: example %d has no answer for %q", i, id)
			}
			row[j] = Clamp(v)
		}
		xs = append(xs, row)
		if ex.Label {
			ys = append(ys, 1)
			positives++
		} else {
			ys = append(ys, 0)
		}
		w := ex.Weight
		if w <= 0 {
			w = 1
		}
		ws = append(ws, w)
	}

	baseRate := float64(positives) / float64(len(examples))
	if positives == 0 || positives == len(examples) {
		return Report{}, fmt.Errorf(
			"decision: calibrate: every example has the same label (%d positive of %d); "+
				"there is nothing to separate", positives, len(examples))
	}

	// Split deterministically by stride rather than shuffling, so the result
	// is reproducible without carrying a seed.
	split := len(xs)
	if o.HoldoutFraction > 0 {
		split = int(float64(len(xs)) * (1 - o.HoldoutFraction))
		if split < 1 {
			split = 1
		}
	}
	trainX, trainY, trainW := xs[:split], ys[:split], ws[:split]
	holdX, holdY := xs[split:], ys[split:]

	coef, intercept := fitLogistic(trainX, trainY, trainW, o)

	threshold, holdAcc := bestThreshold(holdX, holdY, coef, intercept)
	if len(holdX) == 0 {
		threshold, holdAcc = bestThreshold(trainX, trainY, coef, intercept)
	}
	trainAcc := accuracyAt(trainX, trainY, coef, intercept, threshold)

	// The majority-class baseline is what any fit must beat to be worth
	// anything. A model that is 95% accurate on data that is 95% negative has
	// learned to say "no".
	//
	// The margin is two standard errors of the accuracy estimate, not a fixed
	// number. A held-out set of 150 examples has a standard error near 4%, so
	// a flat 2% margin — which an earlier version of this used — calls pure
	// noise separable about as often as not. Scaling the bar to the sample
	// size is the difference between a check and a coin flip.
	majority := math.Max(baseRate, 1-baseRate)
	evalN := len(holdX)
	if evalN == 0 {
		evalN = len(trainX)
	}
	stdErr := 0.5 / math.Sqrt(float64(evalN))
	separable := holdAcc > majority+2*stdErr

	raw := make(map[string]float64, len(ids))
	for i, id := range ids {
		raw[id] = coef[i]
	}

	return Report{
		RequiredAccuracy:   majority + 2*stdErr,
		Weights:            normalizePositive(ids, coef),
		Raw:                raw,
		Intercept:          intercept,
		TrainAccuracy:      trainAcc,
		HoldoutAccuracy:    holdAcc,
		SuggestedThreshold: threshold,
		Separable:          separable,
		BaseRate:           baseRate,
		Examples:           len(examples),
		Holdout:            len(holdX),
	}, nil
}

// fitLogistic runs gradient descent on the weighted log-likelihood with an L2
// penalty.
func fitLogistic(xs [][]float64, ys, ws []float64, o CalibrationOptions) ([]float64, float64) {
	n := len(xs[0])
	coef := make([]float64, n)
	var intercept float64

	var totalW float64
	for _, w := range ws {
		totalW += w
	}
	if totalW == 0 {
		totalW = 1
	}

	grad := make([]float64, n)
	for range o.Iterations {
		for i := range grad {
			grad[i] = 0
		}
		var gb float64

		for i, row := range xs {
			p := sigmoid(dot(row, coef) + intercept)
			err := (p - ys[i]) * ws[i]
			for j, x := range row {
				grad[j] += err * x
			}
			gb += err
		}

		for j := range coef {
			g := grad[j]/totalW + o.L2*coef[j]
			coef[j] -= o.LearningRate * g
		}
		intercept -= o.LearningRate * gb / totalW
	}
	return coef, intercept
}

// normalizePositive turns fitted coefficients into Policy weights.
//
// Negative coefficients are dropped, not negated: a Policy's weighted sum has
// no way to express "this signal argues against", and silently flipping the
// sign would invert the meaning. They remain visible in Report.Raw, where a
// reader can see that a signal is pointing the other way and decide what to do
// about it.
func normalizePositive(ids []string, coef []float64) Weights {
	out := make(Weights, len(ids))
	var sum float64
	for i, c := range coef {
		if c > 0 {
			out[ids[i]] = c
			sum += c
		}
	}
	if sum == 0 {
		return Weights{}
	}
	for id, v := range out {
		out[id] = v / sum
	}
	return out
}

// bestThreshold scans candidate cutoffs and returns the most accurate.
func bestThreshold(xs [][]float64, ys, coef []float64, intercept float64) (float64, float64) {
	if len(xs) == 0 {
		return 0.5, 0
	}
	best, bestAcc := 0.5, -1.0
	for t := 0.05; t <= 0.95; t += 0.01 {
		if acc := accuracyAt(xs, ys, coef, intercept, t); acc > bestAcc {
			best, bestAcc = t, acc
		}
	}
	return math.Round(best*100) / 100, bestAcc
}

func accuracyAt(xs [][]float64, ys, coef []float64, intercept, threshold float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var correct int
	for i, row := range xs {
		p := sigmoid(dot(row, coef) + intercept)
		predicted := 0.0
		if p >= threshold {
			predicted = 1
		}
		if predicted == ys[i] {
			correct++
		}
	}
	return float64(correct) / float64(len(xs))
}

func dot(a, b []float64) float64 {
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

func sigmoid(z float64) float64 {
	// Split by sign to avoid overflow in Exp for large |z|.
	if z >= 0 {
		return 1 / (1 + math.Exp(-z))
	}
	e := math.Exp(z)
	return e / (1 + e)
}
