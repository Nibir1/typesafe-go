package a

import typesafe "github.com/nibir1/typesafe-go"

const (
	spamThreshold   = 0.85
	urgencyCutoff   = 0.7
	uncertaintyBand = 0.1
)

// --- positives ------------------------------------------------------------

// Discarding the second result hides the difference between "confidence zero"
// and "this primitive has none".
func discardsOK(r *typesafe.SystemOneResponse) float64 {
	c, _ := r.Confidence("q") // want `discarding the second result`
	return c
}

func discardsOKAgain(r *typesafe.SystemOneResponse) bool {
	conf, _ := r.Confidence("topic") // want `discarding the second result`
	return conf > 0.5
}

// Bare thresholds drift apart from their siblings.
func bareBool(a typesafe.NoulAnswer) bool {
	return a.Bool(0.85) // want `bare literal`
}

func bareBoolAgain(a typesafe.NoulAnswer) bool {
	return a.Bool(0.5) // want `bare literal`
}

func bareUncertain(a typesafe.NoulAnswer) bool {
	return a.Uncertain(0.1) // want `bare literal`
}

// Branching on a Choice without ever reading its confidence.
func routeIgnoringConfidence(a typesafe.ChoiceAnswer) string {
	switch a.Choice { // want `never reads its Confidence`
	case "billing":
		return "queue-billing"
	default:
		return "queue-other"
	}
}

func branchOnChoice(a typesafe.ChoiceAnswer) string {
	if a.Choice == "technical" { // want `never reads its Confidence`
		return "eng"
	}
	return ""
}

func branchOnScore(a typesafe.ScoreAnswer) bool {
	if a.Score > 1.5 { // want `never reads its Confidence`
		return true
	}
	return false
}

func branchOnNearest(a typesafe.ScoreAnswer) string {
	if i, _ := a.Nearest(); i > 1 { // want `never reads its Confidence`
		return "high"
	}
	return "low"
}

func branchInLoop(a typesafe.ChoiceAnswer) int {
	n := 0
	for a.Choice != "" { // want `never reads its Confidence`
		n++
		break
	}
	return n
}

func branchInCase(a typesafe.ChoiceAnswer) string {
	switch {
	case a.Choice == "billing": // want `never reads its Confidence`
		return "b"
	}
	return ""
}

// In a closure, too.
var _ = func(a typesafe.ChoiceAnswer) string {
	if a.Choice == "x" { // want `never reads its Confidence`
		return "x"
	}
	return ""
}

func scoreAndChoice(c typesafe.ChoiceAnswer, s typesafe.ScoreAnswer) string {
	if s.Score > 1 { // want `never reads its Confidence`
		return c.Choice
	}
	return ""
}

func bareBoolInBranch(a typesafe.NoulAnswer) string {
	if a.Bool(0.95) { // want `bare literal`
		return "yes"
	}
	return "no"
}

func discardsInAssignment(r *typesafe.SystemOneResponse) {
	var c float64
	c, _ = r.Confidence("x") // want `discarding the second result`
	_ = c
}

// --- negatives ------------------------------------------------------------

// Checking the second result is the whole point.
func checksOK(r *typesafe.SystemOneResponse) float64 {
	c, ok := r.Confidence("q")
	if !ok {
		return 0
	}
	return c
}

// A named constant is reviewable.
func namedThreshold(a typesafe.NoulAnswer) bool { return a.Bool(spamThreshold) }

func namedUncertain(a typesafe.NoulAnswer) bool { return a.Uncertain(uncertaintyBand) }

func namedFromVariable(a typesafe.NoulAnswer, t float64) bool { return a.Bool(t) }

// Reading confidence first, branching after, is the normal correct shape.
func routeWithConfidence(a typesafe.ChoiceAnswer) string {
	if a.Confidence < 0.8 {
		return "queue-human"
	}
	switch a.Choice {
	case "billing":
		return "queue-billing"
	default:
		return "queue-other"
	}
}

func scoreWithConfidence(a typesafe.ScoreAnswer) bool {
	if a.Confidence < 0.5 {
		return false
	}
	return a.Score > 1.5
}

// Confidence read after the branch still counts: the function consults it.
func confidenceAfter(a typesafe.ChoiceAnswer) (string, float64) {
	choice := a.Choice
	return choice, a.Confidence
}

// A Noul answer has no confidence to check, so branching on it is fine.
func noulBranch(a typesafe.NoulAnswer) bool {
	return a.Noul > spamThreshold
}

// A suppression is honored.
func suppressed(a typesafe.ChoiceAnswer) string {
	//nolint:confidencecheck the caller gates on confidence before we get here
	return a.Choice
}

// Something else entirely named Confidence is not ours.
type other struct{ Confidence float64 }

func otherConfidence(o other) float64 {
	c, _ := split()
	_ = c
	return o.Confidence
}

func split() (float64, bool) { return 0, false }

// --- deliberately not flagged: reads that are not branches ----------------
//
// These do act on an answer, and a more aggressive rule would flag them. It
// would also flag every accessor in the decision package and the
// implementation of Nearest itself, which is what an earlier version did.
//
// Precision is worth more than recall here: a linter that fires on its own
// library gets switched off, and then catches nothing at all. The branch cases
// above are the ones that matter — code that steers on an answer it has not
// checked.

func returnsComparison(a typesafe.ChoiceAnswer) bool { return a.Choice == "technical" }

func returnsDerived(a typesafe.ScoreAnswer) bool { return a.Score > 1.5 }

func assignsNearest(a typesafe.ScoreAnswer) int {
	i, _ := a.Nearest()
	return i
}

func assignsMostLikely(a typesafe.ScoreAnswer) int {
	i, _ := a.MostLikely()
	return i
}

// A method on an answer type is the implementation, not a caller.
type localScore = typesafe.ScoreAnswer

// A function that touches no answers at all.
func unrelated(x int) int { return x * 2 }

func alsoUnrelated(s string) string { return s + "!" }
