package typesafe

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Answer errors.
var (
	// ErrNoSuchAnswer means the response carried no answer under that id.
	ErrNoSuchAnswer = errors.New("typesafe: no answer with that id")

	// ErrWrongAnswerType means the answer exists but is a different primitive
	// than the accessor asked for.
	ErrWrongAnswerType = errors.New("typesafe: wrong answer type")
)

// Answer is a decoded answer: NoulAnswer, ChoiceAnswer, or ScoreAnswer.
//
// The interface is sealed, so a type switch over it is exhaustive:
//
//	switch a := ans.(type) {
//	case typesafe.NoulAnswer:   // a.Noul
//	case typesafe.ChoiceAnswer: // a.Choice, a.Confidence
//	case typesafe.ScoreAnswer:  // a.Score, a.Confidence
//	}
type Answer interface {
	isAnswer()

	// Type reports the wire discriminator: "noul", "choice", or "score".
	Type() string
}

// Noul decodes the answer under id as a NoulAnswer.
//
// Returns ErrNoSuchAnswer if no such answer exists, or ErrWrongAnswerType if
// it is a different primitive. Both are returned, never panicked: a wrong
// accessor is a coding mistake worth an error, not a crashed process.
func (r *SystemOneResponse) Noul(id string) (NoulAnswer, error) {
	var out NoulAnswer
	raw, err := r.rawOfType(id, TypeNoul)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("typesafe: decoding noul answer %q: %w", id, err)
	}
	return out, nil
}

// Choice decodes the answer under id as a ChoiceAnswer.
func (r *SystemOneResponse) Choice(id string) (ChoiceAnswer, error) {
	var out ChoiceAnswer
	raw, err := r.rawOfType(id, TypeChoice)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("typesafe: decoding choice answer %q: %w", id, err)
	}
	return out, nil
}

// Score decodes the answer under id as a ScoreAnswer.
func (r *SystemOneResponse) Score(id string) (ScoreAnswer, error) {
	var out ScoreAnswer
	raw, err := r.rawOfType(id, TypeScore)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("typesafe: decoding score answer %q: %w", id, err)
	}
	return out, nil
}

// Answer decodes the answer under id into its concrete type, discovered from
// the wire discriminator.
//
// Use this when the question type is not known statically; use Noul, Choice,
// or Score when it is.
func (r *SystemOneResponse) Answer(id string) (Answer, error) {
	raw, ok := r.Answers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNoSuchAnswer, id)
	}
	switch t := answerType(raw); t {
	case TypeNoul:
		return r.Noul(id)
	case TypeChoice:
		return r.Choice(id)
	case TypeScore:
		return r.Score(id)
	default:
		return nil, fmt.Errorf("typesafe: answer %q has unknown type %q", id, t)
	}
}

// All decodes every answer, keyed by question id.
//
// An answer whose type this SDK does not recognize is skipped rather than
// failing the whole call, so a new primitive added server-side degrades to
// "not visible here" instead of breaking existing code. Reach it through
// SystemOneResponse.Answers, which always holds the raw JSON.
func (r *SystemOneResponse) All() map[string]Answer {
	out := make(map[string]Answer, len(r.Answers))
	for id := range r.Answers {
		if a, err := r.Answer(id); err == nil {
			out[id] = a
		}
	}
	return out
}

// Nouls decodes every Noul answer, mirroring the Python SDK's grouped
// accessors. Answers of other types are omitted.
func (r *SystemOneResponse) Nouls() map[string]NoulAnswer {
	out := make(map[string]NoulAnswer)
	for id, raw := range r.Answers {
		if answerType(raw) != TypeNoul {
			continue
		}
		if a, err := r.Noul(id); err == nil {
			out[id] = a
		}
	}
	return out
}

// Choices decodes every Choice answer. Answers of other types are omitted.
func (r *SystemOneResponse) Choices() map[string]ChoiceAnswer {
	out := make(map[string]ChoiceAnswer)
	for id, raw := range r.Answers {
		if answerType(raw) != TypeChoice {
			continue
		}
		if a, err := r.Choice(id); err == nil {
			out[id] = a
		}
	}
	return out
}

// Scores decodes every Score answer. Answers of other types are omitted.
func (r *SystemOneResponse) Scores() map[string]ScoreAnswer {
	out := make(map[string]ScoreAnswer)
	for id, raw := range r.Answers {
		if answerType(raw) != TypeScore {
			continue
		}
		if a, err := r.Score(id); err == nil {
			out[id] = a
		}
	}
	return out
}

// Confidence returns the confidence of the answer under id.
//
// The second result is false for a Noul, which carries no confidence — the
// probability itself expresses the uncertainty. Callers that treat a missing
// confidence as zero would read every Noul as maximally uncertain, so this
// reports absence rather than substituting a number.
func (r *SystemOneResponse) Confidence(id string) (float64, bool) {
	a, err := r.Answer(id)
	if err != nil {
		return 0, false
	}
	switch v := a.(type) {
	case ChoiceAnswer:
		return v.Confidence, true
	case ScoreAnswer:
		return v.Confidence, true
	default:
		return 0, false
	}
}

func (r *SystemOneResponse) rawOfType(id, want string) (json.RawMessage, error) {
	raw, ok := r.Answers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNoSuchAnswer, id)
	}
	if got := answerType(raw); got != want {
		return nil, fmt.Errorf("%w: answer %q is a %s, not a %s", ErrWrongAnswerType, id, got, want)
	}
	return raw, nil
}

// answerType reads the discriminator without decoding the rest.
func answerType(raw json.RawMessage) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.Type
}
