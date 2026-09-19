package typesafe

import (
	"fmt"
	"sort"
	"strings"
)

// Warning is something legal that is probably not what you meant.
//
// Warnings never block a request. The rule this SDK follows: if the server
// would accept it, we send it. Rejecting a request the API would have answered
// is a worse failure than passing through something odd, because the caller
// can see an odd answer and cannot see a request we refused to make.
type Warning struct {
	// QuestionID is the question this concerns, or "" for the request itself.
	QuestionID string

	// Message describes what looks wrong.
	Message string
}

func (w Warning) String() string {
	if w.QuestionID == "" {
		return w.Message
	}
	return fmt.Sprintf("question %q: %s", w.QuestionID, w.Message)
}

// Validate checks a request before it is sent.
//
// The error is returned for anything the server is known to reject; every such
// rule below was confirmed against the live API, not inferred from the docs.
// Warnings are for the legal-but-suspicious, and are advisory.
//
//	warnings, err := req.Validate()
//	if err != nil {
//	    return err
//	}
//	for _, w := range warnings {
//	    log.Warn(w.String())
//	}
//
// SystemOne applies the error half automatically. Call this yourself when you
// want the warnings, or to check a request you build ahead of time.
func (r *SystemOneRequest) Validate() ([]Warning, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	if r.State == nil {
		return nil, fmt.Errorf("%w: state is required", ErrInvalidRequest)
	}
	if len(r.Questions) == 0 {
		return nil, fmt.Errorf("%w: at least one question is required", ErrInvalidRequest)
	}

	var warnings []Warning
	// Deterministic order, so output is stable across runs.
	ids := make([]string, 0, len(r.Questions))
	for id := range r.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("%w: question ids must not be empty", ErrInvalidRequest)
		}
		w, err := validateQuestion(id, r.Questions[id])
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
	}
	return warnings, nil
}

func validateQuestion(id string, q Question) ([]Warning, error) {
	var warnings []Warning

	// A wrapper — a fluent builder, or a typed question — is the struct it
	// stands for. Check anything only the wrapper can know about first, then
	// validate the question underneath it.
	if v, ok := q.(selfValidator); ok {
		if err := v.validateSelf(); err != nil {
			return nil, fmt.Errorf("%w: question %q: %w", ErrInvalidRequest, id, err)
		}
	}
	if b, ok := q.(questionBuilder); ok {
		q = b.question()
	}

	switch v := q.(type) {
	case nil:
		return nil, fmt.Errorf("%w: question %q is nil", ErrInvalidRequest, id)

	case Noul:
		if v.Instructions == nil && v.Criteria == nil {
			warnings = append(warnings, Warning{id,
				"a Noul with neither instructions nor criteria says nothing about what to judge"})
		}

	case Choice:
		// Required and non-empty: the API rejects a Choice without criteria
		// with 422 at ["body","questions",<id>,"choice","criteria"].
		if len(v.Criteria) == 0 {
			return nil, fmt.Errorf("%w: question %q: a Choice needs at least one option",
				ErrInvalidRequest, id)
		}
		for opt := range v.Criteria {
			if strings.TrimSpace(opt) == "" {
				return nil, fmt.Errorf("%w: question %q: option names must not be empty",
					ErrInvalidRequest, id)
			}
		}
		if len(v.Criteria) == 1 {
			warnings = append(warnings, Warning{id,
				"a Choice with one option has nothing to choose between; the answer is foregone"})
		}
		if v.Instructions == nil {
			warnings = append(warnings, Warning{id,
				"a Choice with no instructions is decided from the option names alone"})
		}

	case Score:
		// Both bounds were established empirically. Under-length is a 422;
		// over-length is a 400 that no published source mentions.
		switch n := len(v.Criteria); {
		case n < MinScoreLevels:
			return nil, fmt.Errorf("%w: question %q: a Score needs at least %d level",
				ErrInvalidRequest, id, MinScoreLevels)
		case n > MaxScoreLevels:
			return nil, fmt.Errorf(
				"%w: question %q: a Score accepts at most %d levels, got %d "+
					"(the server rejects more with 400; this is documented nowhere but the wire)",
				ErrInvalidRequest, id, MaxScoreLevels, n)
		case n == 1:
			warnings = append(warnings, Warning{id,
				"a Score with one level always returns 0 with confidence 1; the rubric has nowhere to go"})
		}
		for i, lvl := range v.Criteria {
			if lvl == nil {
				warnings = append(warnings, Warning{id,
					fmt.Sprintf("level %d has no description; its meaning rests entirely on its position", i)})
			}
		}
		if v.Instructions == nil {
			warnings = append(warnings, Warning{id,
				"a Score with no instructions is rated from the level descriptions alone"})
		}

	case RawQuestion:
		// Deliberately unvalidated — the escape hatch exists precisely to send
		// shapes this SDK does not model. One exception: without a type the
		// server cannot dispatch it at all.
		if t, ok := v["type"].(string); !ok || t == "" {
			return nil, fmt.Errorf("%w: question %q: a RawQuestion must set a non-empty \"type\"",
				ErrInvalidRequest, id)
		}

	default:
		return nil, fmt.Errorf("%w: question %q has unknown type %T", ErrInvalidRequest, id, q)
	}

	return warnings, nil
}
