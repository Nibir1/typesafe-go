package a

import typesafe "github.com/nibir1/typesafe-go"

// --- positives: documented failure modes ----------------------------------

var _ = typesafe.Noul{
	Instructions: "How many fruits are in the list?", // want `does not count reliably`
}

var _ = typesafe.Score{
	Instructions: "What is the number of open tickets?", // want `does not count reliably`
	Criteria:     typesafe.Levels{"few", "many"},
}

var _ = typesafe.Noul{
	Instructions: "Count the occurrences of the word refund.", // want `does not count reliably`
}

var _ = typesafe.Noul{
	Instructions: "How often does the customer complain?", // want `does not count reliably`
}

var _ = typesafe.Noul{
	Instructions: "Which date came first, the invoice or the payment?", // want `reads dates as text`
}

var _ = typesafe.Noul{
	Instructions: "Is the due date earlier than the shipping date?", // want `reads dates as text`
}

var _ = typesafe.Score{
	Instructions: "How many days between the order and the refund?", // want `does not count reliably`
	Criteria:     typesafe.Levels{"a", "b"},
}

var _ = typesafe.Noul{
	Instructions: "Is the event within the last quarter?", // want `reads dates as text`
}

var _ = typesafe.Choice{
	Instructions: "Which hex code is closest to the brand colour?", // want `semantic representations`
	Criteria:     typesafe.Options{"a": nil, "b": nil},
}

var _ = typesafe.Noul{
	Instructions: "Do these two rgb values look similar?", // want `semantic representations`
}

var _ = typesafe.Noul{
	Instructions: "Is it not un-reasonable to refund this?", // want `Double negatives`
}

var _ = typesafe.Noul{
	Instructions: "Is the claim never not supported by the policy?", // want `Double negatives`
}

var _ = typesafe.Noul{
	Instructions: "Write a summary of the complaint.", // want `not text`
}

var _ = typesafe.Score{
	Instructions: "Summarize the customer's tone.", // want `not text`
	Criteria:     typesafe.Levels{"a", "b"},
}

var _ = typesafe.Noul{
	Instructions: "Calculate the refund owed to the customer.", // want `not a calculator`
}

var _ = typesafe.Noul{
	Instructions: "What percentage of the order was refunded?", // want `not a calculator`
}

// Inverted criteria: true describes an absence, false a presence.
var _ = typesafe.Noul{
	Instructions: "Assess the message.",
	Criteria: &typesafe.NoulCriteria{
		True:  "The message contains no unsolicited advertising", // want `look inverted`
		False: "The message is unsolicited advertising",
	},
}

//nolint:jaggededge
var _ = typesafe.Noul{
	Instructions: "How many items are in the cart?", // want `without a justification`
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

// "account number" is a noun, not a request to count.
var _ = typesafe.Noul{Instructions: "Does the message include an account number?"}

// Mentioning a date is fine; comparing two is not.
var _ = typesafe.Noul{Instructions: "Does the message mention a delivery date?"}

// Consistent criteria, both phrased the same way round.
var _ = typesafe.Noul{
	Instructions: "Is this unsolicited advertising?",
	Criteria: &typesafe.NoulCriteria{
		True:  "Unsolicited advertising",
		False: "A legitimate conversation",
	},
}

// Both sides negative is consistent, not inverted.
var _ = typesafe.Noul{
	Instructions: "Is the field absent?",
	Criteria: &typesafe.NoulCriteria{
		True:  "The field is not present",
		False: "The field is not missing",
	},
}

// Runtime-computed instructions must not be guessed at.
func dynamic(s string) typesafe.Noul { return typesafe.Noul{Instructions: s} }

// A justified suppression is accepted in silence.
//
//nolint:jaggededge counts are bounded at three here and verified in code
var _ = typesafe.Noul{Instructions: "How many attachments are there?"}

var _ = typesafe.Noul{Instructions: "Does the tone read as hostile?"}

var _ = typesafe.Choice{
	Instructions: "What is the sentiment?",
	Criteria:     typesafe.Options{"positive": nil, "negative": nil, "neutral": nil},
}

var _ = typesafe.Score{
	Instructions: "How urgent is this request?",
	Criteria:     typesafe.Levels{"Can wait", "This week", "Today"},
}

var _ = typesafe.Noul{Instructions: "Has the customer asked for a manager?"}

var _ = typesafe.Noul{Instructions: "Is the policy applicable to this order?"}
