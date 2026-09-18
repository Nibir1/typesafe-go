package typesafe

import (
	"fmt"
	"sort"
	"strconv"
)

// Compile-time typed questions.
//
// A Choice's options and a Score's levels are strings on the wire, which means
// a typo in an option key is invisible until the answer comes back naming a
// branch your switch does not have. These wrappers move that check to the
// compiler: declare the option set as a named type, and the compiler rejects
// anything outside it at both ends — where the question is built and where the
// answer is read.
//
// They are wrappers, not a parallel implementation. Each embeds the plain
// question, so it marshals through exactly the same code and produces
// byte-identical JSON. A golden test asserts it.
//
// There is no TypedNoul. A Noul's answer is a probability, with no key to get
// wrong, so there is nothing for a type parameter to protect.

// OptionKey constrains a Choice's option type: any named string type.
//
//	type Topic string
type OptionKey interface {
	~string
}

// LevelKey constrains a Score's level type: any named integer type.
//
// A Score level's position is its score, so the enum's values must be its
// indices — the usual iota declaration, lowest level first. TypedScore checks
// that and reports a mismatch through Validate rather than silently mapping
// answers to the wrong label.
type LevelKey interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

// --- Choice ------------------------------------------------------------------

// TypedOption is one option of a TypedChoice: a typed name and its
// description.
type TypedOption[T OptionKey] struct {
	// Name is the option, as a value of your named type.
	Name T

	// Description says when the option applies. nil is sent as JSON null,
	// meaning the option is read by its name alone.
	Description EntryType
}

// OptionOf builds one typed option.
//
//	typesafe.OptionOf(TopicBilling, "Invoices, charges, refunds")
//	typesafe.OptionOf(TopicOther, nil) // read by its name alone
//
// Named OptionOf rather than Option because Option is already this package's
// client-configuration type (WithAPIKey and the rest return one).
func OptionOf[T OptionKey](name T, description EntryType) TypedOption[T] {
	return TypedOption[T]{Name: name, Description: description}
}

// TypedChoiceQuestion is a Choice whose options are values of T.
//
// It embeds Choice, so it is a Question, it marshals identically, and every
// plain-Choice field remains reachable.
type TypedChoiceQuestion[T OptionKey] struct {
	Choice

	// options is the declared set, in declaration order, kept so Exhaustive
	// and Answer can check what came back against what was asked.
	options []T
}

// TypedChoice builds a Choice whose options are values of T.
//
//	type Topic string
//	const (
//	    TopicBilling   Topic = "billing"
//	    TopicTechnical Topic = "technical"
//	    TopicOther     Topic = "other"
//	)
//
//	q := typesafe.TypedChoice[Topic]("Which team should handle this?",
//	    typesafe.OptionOf(TopicBilling, "Invoices, charges, refunds"),
//	    typesafe.OptionOf(TopicTechnical, "Bugs, outages, API errors"),
//	    typesafe.OptionOf(TopicOther, nil),
//	)
//
// A duplicate option name is a programming error that would silently drop an
// option from the request; Validate reports it rather than sending a question
// with fewer options than the code appears to declare.
func TypedChoice[T OptionKey](instructions EntryType, opts ...TypedOption[T]) TypedChoiceQuestion[T] {
	criteria := make(Options, len(opts))
	names := make([]T, 0, len(opts))
	for _, o := range opts {
		criteria[string(o.Name)] = o.Description
		names = append(names, o.Name)
	}
	return TypedChoiceQuestion[T]{
		Choice:  Choice{Instructions: instructions, Criteria: criteria},
		options: names,
	}
}

// Options returns the declared option set, in declaration order.
func (q TypedChoiceQuestion[T]) Options() []T {
	out := make([]T, len(q.options))
	copy(out, q.options)
	return out
}

// question exposes the underlying Choice to validation, so a typed question
// gets exactly the same construction-time checks as the plain one.
func (q TypedChoiceQuestion[T]) question() Question { return q.Choice }

// validateSelf reports a declared set that cannot round-trip.
func (q TypedChoiceQuestion[T]) validateSelf() error {
	if len(q.options) != len(q.Criteria) {
		seen := make(map[T]bool, len(q.options))
		for _, o := range q.options {
			if seen[o] {
				return fmt.Errorf("option %q is declared twice, so the request carries "+
					"fewer options than the code appears to declare", string(o))
			}
			seen[o] = true
		}
	}
	return nil
}

// Answer decodes the answer under id as a ChoiceAnswerOf[T], and checks it
// against this question's declared option set.
//
//	ans, err := q.Answer(resp, "department")
//	switch ans.Choice {      // Topic, not string
//	case TopicBilling:   ...
//	case TopicTechnical: ...
//	}
//
// An answer naming an option the question did not declare is an error. That
// can only happen if the request and the type have drifted apart, and it is
// exactly the case a switch would handle by silently falling through.
func (q TypedChoiceQuestion[T]) Answer(r *SystemOneResponse, id string) (ChoiceAnswerOf[T], error) {
	ans, err := TypedChoiceAnswer[T](r, id)
	if err != nil {
		return ans, err
	}
	if err := Exhaustive(ans, q.options...); err != nil {
		return ans, err
	}
	return ans, nil
}

// ChoiceAnswerOf is a ChoiceAnswer whose option keys are values of T.
type ChoiceAnswerOf[T OptionKey] struct {
	// Choice is the highest-probability option, as a T.
	Choice T

	// Probabilities gives every option's probability, keyed by T.
	Probabilities map[T]float64

	// Confidence in [0,1], derived from the shape of the distribution.
	Confidence float64
}

// TypedChoiceAnswer decodes the answer under id with T as the option type.
//
//	ans, err := typesafe.TypedChoiceAnswer[Topic](resp, "department")
//
// This is the free-standing form, for a response you did not build the
// question for. It converts whatever came back into T without checking it
// against a declared set — nothing here knows what that set was. When you have
// the question, prefer its Answer method, which checks; otherwise pass your
// enum's values to Exhaustive.
func TypedChoiceAnswer[T OptionKey](r *SystemOneResponse, id string) (ChoiceAnswerOf[T], error) {
	var out ChoiceAnswerOf[T]
	raw, err := r.Choice(id)
	if err != nil {
		return out, err
	}
	out.Choice = T(raw.Choice)
	out.Confidence = raw.Confidence
	out.Probabilities = make(map[T]float64, len(raw.Probabilities))
	for k, p := range raw.Probabilities {
		out.Probabilities[T(k)] = p
	}
	return out, nil
}

// Untyped returns the plain ChoiceAnswer, for the accessors that do not need
// the type parameter and for code that has not been converted yet.
func (a ChoiceAnswerOf[T]) Untyped() ChoiceAnswer {
	probs := make(map[string]float64, len(a.Probabilities))
	for k, p := range a.Probabilities {
		probs[string(k)] = p
	}
	return ChoiceAnswer{
		Choice:        string(a.Choice),
		Probabilities: probs,
		Confidence:    a.Confidence,
	}
}

// RankedOptionOf is one typed option and its probability.
type RankedOptionOf[T OptionKey] struct {
	Option      T
	Probability float64
}

// Ranked returns every option ordered by descending probability, ties broken
// by name so the order is stable across runs.
func (a ChoiceAnswerOf[T]) Ranked() []RankedOptionOf[T] {
	out := make([]RankedOptionOf[T], 0, len(a.Probabilities))
	for opt, p := range a.Probabilities {
		out = append(out, RankedOptionOf[T]{Option: opt, Probability: p})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Probability != out[j].Probability {
			return out[i].Probability > out[j].Probability
		}
		return out[i].Option < out[j].Option
	})
	return out
}

// Margin is the gap between the top two options.
func (a ChoiceAnswerOf[T]) Margin() float64 { return a.Untyped().Margin() }

// ProbabilityOf returns an option's probability, and whether it was part of
// the answer at all.
func (a ChoiceAnswerOf[T]) ProbabilityOf(option T) (float64, bool) {
	p, ok := a.Probabilities[option]
	return p, ok
}

// ErrUndeclaredOption means an answer named something outside the declared set.
var ErrUndeclaredOption = fmt.Errorf("typesafe: answer contains an option the question did not declare")

// Exhaustive checks that every option in the answer is one of allowed.
//
//	if err := typesafe.Exhaustive(ans, AllTopics...); err != nil {
//	    return err
//	}
//
// It returns an error rather than panicking, and it is worth calling: the only
// way an answer can carry an undeclared option is that the request and the
// type have drifted apart — a question built somewhere else, or an enum that
// gained a value the question was never updated with. A type switch handles
// that by falling through to no branch at all, silently.
//
// Passing no allowed values checks nothing and returns nil, so a caller that
// has not enumerated its set is not forced to.
func Exhaustive[T OptionKey](a ChoiceAnswerOf[T], allowed ...T) error {
	if len(allowed) == 0 {
		return nil
	}
	set := make(map[T]bool, len(allowed))
	for _, v := range allowed {
		set[v] = true
	}

	var undeclared []string
	for opt := range a.Probabilities {
		if !set[opt] {
			undeclared = append(undeclared, string(opt))
		}
	}
	if !set[a.Choice] {
		if !contains(undeclared, string(a.Choice)) {
			undeclared = append(undeclared, string(a.Choice))
		}
	}
	if len(undeclared) == 0 {
		return nil
	}
	sort.Strings(undeclared) // map order is random; the message must not be
	return fmt.Errorf("%w: %v", ErrUndeclaredOption, undeclared)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// --- Score -------------------------------------------------------------------

// TypedLevel is one level of a TypedScore: a typed level and its description.
type TypedLevel[L LevelKey] struct {
	// Level is the rubric level, as a value of your named type. Its numeric
	// value must equal its position, lowest first.
	Level L

	// Description says what this level means.
	Description EntryType
}

// LevelOf builds one typed level.
//
//	typesafe.LevelOf(FrustrationCalm, "No sign of irritation")
func LevelOf[L LevelKey](level L, description EntryType) TypedLevel[L] {
	return TypedLevel[L]{Level: level, Description: description}
}

// TypedScoreQuestion is a Score whose levels are values of L.
type TypedScoreQuestion[L LevelKey] struct {
	Score

	// levels is the declared rubric in order, kept so answers can map an
	// index back to its label.
	levels []L
}

// TypedScore builds a Score whose levels are values of L.
//
//	type Frustration int
//	const (
//	    Calm Frustration = iota
//	    Annoyed
//	    Angry
//	)
//
//	q := typesafe.TypedScore[Frustration]("How frustrated is the customer?",
//	    typesafe.LevelOf(Calm, "No sign of irritation"),
//	    typesafe.LevelOf(Annoyed, "Clearly unhappy, still civil"),
//	    typesafe.LevelOf(Angry, "Hostile, threatening to leave"),
//	)
//
// Position is meaning: the first level scores 0, the second 1, and so on. The
// enum's values must therefore match their positions, which an ordinary iota
// declaration gives you. Validate reports a mismatch — passing levels out of
// order would map every answer to the wrong label, and nothing in the
// resulting score would reveal it.
func TypedScore[L LevelKey](instructions EntryType, levels ...TypedLevel[L]) TypedScoreQuestion[L] {
	criteria := make(Levels, 0, len(levels))
	labels := make([]L, 0, len(levels))
	for _, l := range levels {
		criteria = append(criteria, l.Description)
		labels = append(labels, l.Level)
	}
	return TypedScoreQuestion[L]{
		Score:  Score{Instructions: instructions, Criteria: criteria},
		levels: labels,
	}
}

// Levels returns the declared levels, in rubric order.
func (q TypedScoreQuestion[L]) Levels() []L {
	out := make([]L, len(q.levels))
	copy(out, q.levels)
	return out
}

// question exposes the underlying Score to validation.
func (q TypedScoreQuestion[L]) question() Question { return q.Score }

// validateSelf reports a rubric whose labels do not match their positions.
func (q TypedScoreQuestion[L]) validateSelf() error {
	for i, l := range q.levels {
		if int64(l) != int64(i) {
			return fmt.Errorf("level %d of the rubric has value %d; a Score level's "+
				"position is its score, so the levels must be declared lowest first "+
				"with values matching their positions", i, int64(l))
		}
	}
	return nil
}

// Answer decodes the answer under id as a ScoreAnswerOf[L].
func (q TypedScoreQuestion[L]) Answer(r *SystemOneResponse, id string) (ScoreAnswerOf[L], error) {
	return TypedScoreAnswer[L](r, id)
}

// ScoreAnswerOf is a ScoreAnswer whose level indices are values of L.
type ScoreAnswerOf[L LevelKey] struct {
	// Score is the probability-weighted position across the levels. It is
	// ordinal, and may fall between two levels — which is why it stays a
	// float64 rather than becoming an L.
	Score float64

	// Level is the nearest declared level to Score.
	Level L

	// Legend maps each level to its description.
	Legend map[L]EntryType

	// Probabilities maps each level to its probability. They sum to 1.
	Probabilities map[L]float64

	// Confidence in [0,1], derived from the distribution's shape.
	Confidence float64
}

// TypedScoreAnswer decodes the answer under id with L as the level type.
//
// Level indices come back as strings on the wire — "0", "1" — and are parsed
// as integers, not sorted as strings. An index that does not parse is an error
// rather than a silently dropped level.
func TypedScoreAnswer[L LevelKey](r *SystemOneResponse, id string) (ScoreAnswerOf[L], error) {
	var out ScoreAnswerOf[L]
	raw, err := r.Score(id)
	if err != nil {
		return out, err
	}

	out.Score = raw.Score
	out.Confidence = raw.Confidence
	out.Legend = make(map[L]EntryType, len(raw.Legend))
	for k, v := range raw.Legend {
		i, err := strconv.Atoi(k)
		if err != nil {
			return out, fmt.Errorf("typesafe: score answer %q has a non-numeric legend key %q", id, k)
		}
		out.Legend[L(i)] = v
	}
	out.Probabilities = make(map[L]float64, len(raw.Probabilities))
	for k, p := range raw.Probabilities {
		i, err := strconv.Atoi(k)
		if err != nil {
			return out, fmt.Errorf("typesafe: score answer %q has a non-numeric probability key %q", id, k)
		}
		out.Probabilities[L(i)] = p
	}

	nearest, _ := raw.Nearest()
	if nearest >= 0 {
		out.Level = L(nearest)
	}
	return out, nil
}

// Untyped returns the plain ScoreAnswer.
func (a ScoreAnswerOf[L]) Untyped() ScoreAnswer {
	legend := make(map[string]EntryType, len(a.Legend))
	for k, v := range a.Legend {
		legend[strconv.Itoa(int(k))] = v
	}
	probs := make(map[string]float64, len(a.Probabilities))
	for k, p := range a.Probabilities {
		probs[strconv.Itoa(int(k))] = p
	}
	return ScoreAnswer{
		Score:         a.Score,
		Legend:        legend,
		Probabilities: probs,
		Confidence:    a.Confidence,
	}
}

// AtOrAbove returns the total probability of landing at level or higher.
//
// This is the safe way to threshold a Score: it works on the distribution the
// model produced rather than on an interpolated magnitude the levels do not
// support.
//
//	if ans.AtOrAbove(Angry) > 0.8 { escalate() }
func (a ScoreAnswerOf[L]) AtOrAbove(level L) float64 {
	var sum float64
	for k, p := range a.Probabilities {
		if k >= level {
			sum += p
		}
	}
	return sum
}

// AtOrBelow returns the total probability of landing at level or lower.
func (a ScoreAnswerOf[L]) AtOrBelow(level L) float64 {
	var sum float64
	for k, p := range a.Probabilities {
		if k <= level {
			sum += p
		}
	}
	return sum
}

// MostLikely returns the single highest-probability level and its probability.
//
// This is not always Level: a bimodal distribution has a weighted mean sitting
// in a level the model considers unlikely. When the two disagree, the question
// has more than one reading.
func (a ScoreAnswerOf[L]) MostLikely() (L, float64) {
	var best L
	bestP := -1.0
	first := true
	for _, k := range sortedLevels(a.Probabilities) {
		p := a.Probabilities[k]
		if first || p > bestP {
			best, bestP, first = k, p, false
		}
	}
	if first {
		return best, 0
	}
	return best, bestP
}

// sortedLevels returns a level-keyed map's keys in ascending order, so a
// scan over them is deterministic.
func sortedLevels[L LevelKey, V any](m map[L]V) []L {
	keys := make([]L, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// selfValidator is implemented by question wrappers that can be internally
// inconsistent in a way the plain question cannot express — a duplicate typed
// option, or a rubric whose labels do not match their positions.
//
// These are programming errors, and the alternatives are worse: a constructor
// returning an error would make the fluent form unusable, and a panic would
// crash a process over a mistake that Validate can report like any other.
type selfValidator interface {
	validateSelf() error
}

var (
	_ questionBuilder = TypedChoiceQuestion[string]{}
	_ questionBuilder = TypedScoreQuestion[int]{}
	_ selfValidator   = TypedChoiceQuestion[string]{}
	_ selfValidator   = TypedScoreQuestion[int]{}
)
