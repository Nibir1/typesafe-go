// Package typesafe is a stub for analyzer tests.
//
// analysistest runs in GOPATH mode, so testdata cannot import the real module.
// This provides the same import path and the same type names, which is all the
// analyzers match on — they resolve types through the type checker and compare
// the package path, so a stub at the right path is indistinguishable from the
// real thing for their purposes.
//
// Keeping it minimal is deliberate: these tests should fail when an analyzer
// breaks, not when an unrelated part of the SDK changes.
package typesafe

type EntryType = any

type Question interface{ isQuestion() }

type Noul struct {
	Instructions EntryType
	Criteria     *NoulCriteria
}

func (Noul) isQuestion() {}

type NoulCriteria struct {
	True  EntryType
	False EntryType
}

type Options map[string]EntryType

type Choice struct {
	Instructions EntryType
	Criteria     Options
}

func (Choice) isQuestion() {}

type Levels []EntryType

type Score struct {
	Instructions EntryType
	Criteria     Levels
}

func (Score) isQuestion() {}

type NoulAnswer struct{ Noul float64 }

func (a NoulAnswer) Bool(threshold float64) bool  { return a.Noul >= threshold }
func (a NoulAnswer) Uncertain(delta float64) bool { return false }

type ChoiceAnswer struct {
	Choice        string
	Probabilities map[string]float64
	Confidence    float64
}

type ScoreAnswer struct {
	Score         float64
	Legend        map[string]EntryType
	Probabilities map[string]float64
	Confidence    float64
}

func (a ScoreAnswer) Nearest() (int, EntryType)  { return 0, nil }
func (a ScoreAnswer) MostLikely() (int, float64) { return 0, 0 }

type SystemOneResponse struct {
	Model   string
	Answers map[string]any
}

func (r *SystemOneResponse) Confidence(id string) (float64, bool) { return 0, false }
func (r *SystemOneResponse) Noul(id string) (NoulAnswer, error)   { return NoulAnswer{}, nil }
func (r *SystemOneResponse) Choice(id string) (ChoiceAnswer, error) {
	return ChoiceAnswer{}, nil
}
func (r *SystemOneResponse) Score(id string) (ScoreAnswer, error) { return ScoreAnswer{}, nil }
