package typesafe

// EntryType is the union the API uses for every human-readable field:
//
//	string | object | array | null
//
// It appears as a question's Instructions, as a Choice option's description,
// as a Score level's description, and as a Noul's true/false criteria.
//
// Structured values are a supported feature, not a quirk. A schema, a taxonomy,
// or a database row is already JSON; passing it directly is clearer than
// flattening it into a sentence, and the model is trained to read structure.
//
//	Instructions: "Does this convey urgency?"
//
//	Instructions: map[string]any{
//	    "field":    map[string]any{"name": "amount_due", "unit": "USD"},
//	    "question": "How large is the `field` value in `source_text`?",
//	}
//
// A nil EntryType is omitted from the request. The API treats an absent field
// and an explicit null identically.
type EntryType = any

// Question is one of the three System One primitives: Noul, Choice, or Score.
//
// The interface is sealed — only this package can implement it — so the set of
// question types stays closed and a type switch over them is exhaustive.
// RawQuestion is the escape hatch for anything this SDK does not yet model.
type Question interface {
	isQuestion()
}

// RawQuestion is an arbitrary JSON-encodable question body, sent exactly as
// given.
//
// It exists so a caller is never blocked by this SDK lagging the API: a
// question shape added after this release can be sent today. Nothing is
// validated and nothing is defaulted — including the required "type" field,
// which you must set yourself.
//
//	typesafe.RawQuestion{"type": "noul", "instructions": "Is this spam?"}
//
// Prefer the typed primitives. They validate before the request leaves the
// process, and their answers decode into typed accessors.
type RawQuestion map[string]any

func (RawQuestion) isQuestion() {}
