package typesafe

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
)

// Score rating bounds, both established against the live API rather than from
// documentation.
const (
	// MinScoreLevels is the fewest rubric levels the API accepts.
	//
	// The prose documentation claims two. It is wrong: a one-level Score
	// returns 200 with score 0 and confidence 1. Degenerate, but legal, and
	// this SDK does not reject requests the server would accept.
	MinScoreLevels = 1

	// MaxScoreLevels is the most rubric levels the API accepts. Exceeding it
	// returns 400 "Too many score levels. Must have at most 10 levels."
	//
	// Documented in neither the OpenAPI schema nor the prose docs. Validate
	// enforces it client-side so the failure costs no round trip.
	MaxScoreLevels = 10
)

// Score rates the state against ordered levels you define.
//
//	typesafe.Score{
//	    Instructions: "How frustrated is the customer?",
//	    Criteria:     typesafe.Levels{"Calm", "Frustrated", "Very angry"},
//	}
//
// Order is meaning: a level's position is its score, starting at zero. The
// answer is a probability-weighted value that may land between levels.
//
// # Do not read a magnitude out of a score
//
// TypeSafe documents that Jev's score levels are weakly calibrated
// numerically. Thresholding is sound — "is this at least Frustrated?" — but
// interpolating a real-world quantity between two levels is not. If you need a
// number, extract it as a Choice over enumerated parts and compute in code.
type Score struct {
	// Instructions is what the model should rate. Optional per the schema.
	Instructions EntryType

	// Criteria is the ordered level descriptions, lowest first. Required.
	// Must hold between MinScoreLevels and MaxScoreLevels entries.
	Criteria Levels
}

// Levels is the ordered rubric. Index is the score.
type Levels []EntryType

func (Score) isQuestion() {}

// MarshalJSON emits the wire form, adding the required type discriminator.
func (s Score) MarshalJSON() ([]byte, error) {
	criteria := s.Criteria
	if criteria == nil {
		criteria = Levels{}
	}
	return json.Marshal(struct {
		Type         string    `json:"type"`
		Instructions EntryType `json:"instructions,omitempty"`
		Criteria     Levels    `json:"criteria"`
	}{TypeScore, s.Instructions, criteria})
}

// ScoreAnswer is the answer to a Score.
//
// Legend and Probabilities are keyed by the level index as a string — "0",
// "1", and so on. Use the ordered accessors rather than ranging over the maps:
// Go map order is random, and these keys are integers wearing string clothes.
type ScoreAnswer struct {
	// Score is the probability-weighted position across the levels. It may
	// fall between two of them. Treat it as ordinal, not as a magnitude.
	Score float64 `json:"score"`

	// Legend maps each level index to its description. Values carry whatever
	// shape you supplied as criteria — a structured rubric returns structured
	// legend entries, so this is EntryType and not string.
	Legend map[string]EntryType `json:"legend"`

	// Probabilities maps each level index to its probability. They sum to 1.
	Probabilities map[string]float64 `json:"probabilities"`

	// Confidence in [0,1], derived from the distribution's shape.
	Confidence float64 `json:"confidence"`
}

func (ScoreAnswer) isAnswer()    {}
func (ScoreAnswer) Type() string { return TypeScore }

// NumLevels is the size of the rubric this answer was scored against.
func (a ScoreAnswer) NumLevels() int { return len(a.Legend) }

// Levels returns the legend in index order.
//
// Keys are parsed as integers rather than sorted as strings. With the current
// ten-level ceiling the two orderings coincide, so this is insurance rather
// than a fix — but it is the ordering the data actually means, and it stays
// correct if the ceiling is ever raised.
func (a ScoreAnswer) Levels() []EntryType {
	out := make([]EntryType, 0, len(a.Legend))
	for _, k := range sortedIndexKeys(a.Legend) {
		out = append(out, a.Legend[k])
	}
	return out
}

// LevelProbabilities returns the distribution in index order.
func (a ScoreAnswer) LevelProbabilities() []float64 {
	out := make([]float64, 0, len(a.Probabilities))
	for _, k := range sortedIndexKeys(a.Probabilities) {
		out = append(out, a.Probabilities[k])
	}
	return out
}

// Nearest returns the level index closest to Score, with its description.
//
// Rounds half away from zero. Returns -1 and nil for an empty legend.
func (a ScoreAnswer) Nearest() (int, EntryType) {
	if len(a.Legend) == 0 {
		return -1, nil
	}
	i := int(math.Round(a.Score))
	if i < 0 {
		i = 0
	}
	if max := len(a.Legend) - 1; i > max {
		i = max
	}
	return i, a.Legend[strconv.Itoa(i)]
}

// MostLikely returns the single highest-probability level and its probability.
//
// This is not always Nearest: a bimodal distribution — heavy at both ends,
// light in the middle — has a weighted mean that sits in a level the model
// considers unlikely. When the two disagree, the distribution is telling you
// the question has more than one reading.
func (a ScoreAnswer) MostLikely() (int, float64) {
	best, bestP := -1, math.Inf(-1)
	for _, k := range sortedIndexKeys(a.Probabilities) {
		if p := a.Probabilities[k]; p > bestP {
			i, err := strconv.Atoi(k)
			if err != nil {
				continue
			}
			best, bestP = i, p
		}
	}
	if best < 0 {
		return -1, 0
	}
	return best, bestP
}

// AtOrAbove returns the total probability of landing at level or higher.
//
// This is the safe way to threshold a Score: it works on the distribution the
// model actually produced, rather than on an interpolated magnitude the levels
// do not support.
//
//	if ans.AtOrAbove(2) > 0.8 { escalate() }
func (a ScoreAnswer) AtOrAbove(level int) float64 {
	var sum float64
	for k, p := range a.Probabilities {
		if i, err := strconv.Atoi(k); err == nil && i >= level {
			sum += p
		}
	}
	return sum
}

// AtOrBelow returns the total probability of landing at level or lower.
func (a ScoreAnswer) AtOrBelow(level int) float64 {
	var sum float64
	for k, p := range a.Probabilities {
		if i, err := strconv.Atoi(k); err == nil && i <= level {
			sum += p
		}
	}
	return sum
}

// sortedIndexKeys returns a map's keys ordered numerically, with any
// unparseable key sorted last by string order so nothing is silently dropped.
func sortedIndexKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, errA := strconv.Atoi(keys[i])
		b, errB := strconv.Atoi(keys[j])
		switch {
		case errA == nil && errB == nil:
			return a < b
		case errA == nil:
			return true
		case errB == nil:
			return false
		default:
			return keys[i] < keys[j]
		}
	})
	return keys
}
