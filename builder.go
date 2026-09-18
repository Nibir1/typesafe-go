package typesafe

// Fluent question constructors.
//
// The struct forms remain the documented default — they are plainer, they show
// every field, and they are what the examples use. These exist because
// building a Choice option by option reads better than assembling a map
// literal when the descriptions are long, and because anyone arriving from the
// JavaScript SDK's choice() / noul() / score() will reach for them.
//
// Both forms produce identical wire output. A golden test asserts it, because
// two ways of writing the same request that disagree by a byte would be worse
// than one.

// NewNoul starts a Noul.
//
//	typesafe.NewNoul("Does this convey urgency?").
//	    Means("Explicitly time-sensitive", "No urgency expressed")
func NewNoul(instructions EntryType) NoulBuilder {
	return NoulBuilder{q: Noul{Instructions: instructions}}
}

// NoulBuilder builds a Noul.
//
// The methods return a copy rather than mutating, so a partially built
// question can be shared as a base without one caller's additions leaking into
// another's.
type NoulBuilder struct{ q Noul }

// Means describes what a yes and a no mean.
//
// Keep the two aligned with the instructions. TypeSafe documents that a Noul
// whose true describes a "no" performs measurably worse, and nothing in the
// resulting probability reveals the mistake.
func (b NoulBuilder) Means(yes, no EntryType) NoulBuilder {
	b.q.Criteria = &NoulCriteria{True: yes, False: no}
	return b
}

// Build returns the question.
func (b NoulBuilder) Build() Noul { return b.q }

// NewChoice starts a Choice.
//
//	typesafe.NewChoice("Which team should handle this?").
//	    Option("billing", "Payments, invoicing, refunds").
//	    Option("technical", "Bugs, outages, integrations").
//	    Option("other", nil)
func NewChoice(instructions EntryType) ChoiceBuilder {
	return ChoiceBuilder{q: Choice{Instructions: instructions, Criteria: Options{}}}
}

// ChoiceBuilder builds a Choice.
type ChoiceBuilder struct{ q Choice }

// Option adds an option and its description.
//
// Pass nil for the description when the option's name says everything; it is
// sent as JSON null, which the API reads as "interpret this by its name
// alone".
func (b ChoiceBuilder) Option(name string, description EntryType) ChoiceBuilder {
	// Copy the map so builders derived from a shared base stay independent.
	next := make(Options, len(b.q.Criteria)+1)
	for k, v := range b.q.Criteria {
		next[k] = v
	}
	next[name] = description
	b.q.Criteria = next
	return b
}

// Options adds several options that need no description, for the common case
// where the names are self-explanatory.
//
//	typesafe.NewChoice("What is the tone?").Options("calm", "angry", "excited")
func (b ChoiceBuilder) Options(names ...string) ChoiceBuilder {
	for _, n := range names {
		b = b.Option(n, nil)
	}
	return b
}

// Build returns the question.
func (b ChoiceBuilder) Build() Choice { return b.q }

// NewScore starts a Score.
//
//	typesafe.NewScore("How frustrated is the customer?").
//	    Levels("Calm", "Frustrated", "Very angry")
//
// Order is meaning: a level's position is its score, lowest first.
func NewScore(instructions EntryType) ScoreBuilder {
	return ScoreBuilder{q: Score{Instructions: instructions}}
}

// ScoreBuilder builds a Score.
type ScoreBuilder struct{ q Score }

// Level appends one level. Call it in order, lowest first.
func (b ScoreBuilder) Level(description EntryType) ScoreBuilder {
	next := make(Levels, len(b.q.Criteria), len(b.q.Criteria)+1)
	copy(next, b.q.Criteria)
	b.q.Criteria = append(next, description)
	return b
}

// Levels appends several string levels in order, lowest first.
func (b ScoreBuilder) Levels(descriptions ...string) ScoreBuilder {
	for _, d := range descriptions {
		b = b.Level(d)
	}
	return b
}

// Build returns the question.
func (b ScoreBuilder) Build() Score { return b.q }

// The builders satisfy Question directly, so a Build call is optional:
//
//	Questions: map[string]typesafe.Question{
//	    "team": typesafe.NewChoice("Which team?").Options("billing", "technical"),
//	}

func (b NoulBuilder) isQuestion()   {}
func (b ChoiceBuilder) isQuestion() {}
func (b ScoreBuilder) isQuestion()  {}

// questionBuilder lets validation see the question underneath a builder.
//
// Without it a fluently built question marshals correctly and then fails
// validation as an "unknown type" — the worst of both, since it looks like it
// works right up until the checks that matter. Unwrapping means a builder gets
// exactly the same construction-time checks as the struct it stands for.
type questionBuilder interface {
	question() Question
}

func (b NoulBuilder) question() Question   { return b.q }
func (b ChoiceBuilder) question() Question { return b.q }
func (b ScoreBuilder) question() Question  { return b.q }

var (
	_ questionBuilder = NoulBuilder{}
	_ questionBuilder = ChoiceBuilder{}
	_ questionBuilder = ScoreBuilder{}
)

// MarshalJSON delegates to the built question, so a builder used in place of a
// question produces byte-identical output.
func (b NoulBuilder) MarshalJSON() ([]byte, error) { return b.q.MarshalJSON() }

// MarshalJSON delegates to the built question.
func (b ChoiceBuilder) MarshalJSON() ([]byte, error) { return b.q.MarshalJSON() }

// MarshalJSON delegates to the built question.
func (b ScoreBuilder) MarshalJSON() ([]byte, error) { return b.q.MarshalJSON() }
