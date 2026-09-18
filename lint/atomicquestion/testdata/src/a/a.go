package a

import typesafe "github.com/nibir1/typesafe-go"

const spamThreshold = 0.85

// --- positives: questions that ask more than one thing --------------------

var _ = typesafe.Noul{
	Instructions: "Is this urgent? Does it mention billing?", // want `ask 2 questions`
}

var _ = typesafe.Noul{
	Instructions: "Is this spam and does it contain a link?", // want `join two judgments`
}

var _ = typesafe.Noul{
	Instructions: "Is the customer angry or is the request time-sensitive?", // want `join two judgments`
}

var _ = typesafe.Choice{
	Instructions: "Which team, and whether it is urgent?", // want `join two judgments`
	Criteria:     typesafe.Options{"a": nil, "b": nil},
}

var _ = typesafe.Score{
	Instructions: "How frustrated is the customer as well as how urgent is this?", // want `join two judgments`
	Criteria:     typesafe.Levels{"low", "high"},
}

var _ = typesafe.Noul{
	Instructions: "Does it ask for credentials and then create time pressure?", // want `join two judgments`
}

var _ = typesafe.Noul{
	Instructions: "Is it spam? Is it phishing? Is it a scam?", // want `ask 3 questions`
}

var _ = typesafe.Noul{
	Instructions: "Is the invoice paid and is the customer satisfied?", // want `join two judgments`
}

var _ = typesafe.Choice{
	Instructions: "Pick one.",
	Criteria: typesafe.Options{ // want `offers 13 options`
		"a1": nil, "a2": nil, "a3": nil, "a4": nil, "a5": nil, "a6": nil, "a7": nil,
		"a8": nil, "a9": nil, "b1": nil, "b2": nil, "b3": nil, "b4": nil,
	},
}

var _ = typesafe.Choice{
	Instructions: "Pick one.",
	Criteria:     typesafe.Options{"only": nil}, // want `offers one option`
}

var _ = typesafe.Noul{
	Instructions: "Was it delivered and was it on time?", // want `join two judgments`
}

var _ = typesafe.Noul{
	Instructions: "Does the policy apply or does the exception apply?", // want `join two judgments`
}

var _ = typesafe.Score{
	Instructions: "How severe is it? How bad is the impact?", // want `ask 2 questions`
	Criteria:     typesafe.Levels{"low", "high"},
}

var _ = typesafe.Noul{
	Instructions: "Is it overdue and has it been escalated?", // want `join two judgments`
}

var _ = typesafe.Noul{
	// Concatenation must be folded, or the longest instructions escape checking.
	Instructions: "Is the account locked " + // want `join two judgments`
		"and is the password expired?",
}

//nolint:atomicquestion
var _ = typesafe.Noul{
	Instructions: "Is it signed and is it dated?", // want `without a justification`
}

// --- negatives: these must stay silent ------------------------------------

var _ = typesafe.Noul{Instructions: "Does this convey urgency?"}

var _ = typesafe.Noul{Instructions: "The message requests a refund."}

var _ = typesafe.Choice{
	Instructions: "Which team should handle this?",
	Criteria:     typesafe.Options{"billing": "Payments", "technical": "Bugs"},
}

var _ = typesafe.Score{
	Instructions: "How frustrated is the customer?",
	Criteria:     typesafe.Levels{"Calm", "Frustrated", "Very angry"},
}

// "terms and conditions" is one noun phrase, not two judgments.
var _ = typesafe.Noul{Instructions: "Does this reference the terms and conditions?"}

// "research and development" likewise.
var _ = typesafe.Choice{
	Instructions: "Which department: research and development, or sales?",
	Criteria:     typesafe.Options{"rnd": nil, "sales": nil},
}

// A single question mark is fine.
var _ = typesafe.Noul{Instructions: "Is the tone polite?"}

// Runtime-computed instructions cannot be checked, and must not be guessed at.
func dynamic(s string) typesafe.Noul { return typesafe.Noul{Instructions: s} }

// A structured instruction is not a string and is not word-matched.
var _ = typesafe.Noul{
	Instructions: map[string]any{"field": "amount", "question": "Does it match?"},
}

// Exactly at the option limit, not over it.
var _ = typesafe.Choice{
	Instructions: "Pick one.",
	Criteria: typesafe.Options{
		"a1": nil, "a2": nil, "a3": nil, "a4": nil, "a5": nil, "a6": nil,
		"a7": nil, "a8": nil, "a9": nil, "b1": nil, "b2": nil, "b3": nil,
	},
}

// A justified suppression is accepted in silence.
//
//nolint:atomicquestion the two clauses are one legal test that cannot be split
var _ = typesafe.Noul{Instructions: "Is it signed and is it dated?"}

// A struct of our own that happens to share a name is not a TypeSafe question.
type Noul struct{ Instructions string }

var _ = Noul{Instructions: "Is this urgent? Is it billing?"}

var _ = typesafe.Noul{Instructions: "Does the sender appear to be an executive?"}

var _ = typesafe.Choice{
	Instructions: "What is the tone?",
	Criteria:     typesafe.Options{"calm": nil, "angry": nil, "excited": nil},
}

var _ = typesafe.Score{
	Instructions: "How urgent is this request?",
	Criteria:     typesafe.Levels{"Can wait", "This week", "Today"},
}

var _ = typesafe.Noul{Instructions: "The customer has asked for a manager."}
