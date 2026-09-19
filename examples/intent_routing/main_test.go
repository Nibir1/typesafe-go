package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/examples/internal/harness"
)

func TestRun(t *testing.T) {
	client, _ := harness.Client(t, "testdata/cassettes/intent_routing.jsonl")

	var out bytes.Buffer
	if err := run(context.Background(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	t.Log("\n" + out.String())
}

// Every declared intent must have a handler, and every handler a declared
// intent. This is the drift the typed API exists to prevent, asserted rather
// than assumed.
func TestEveryIntentHasAHandler(t *testing.T) {
	for _, i := range AllIntents {
		if got := dispatch(i); strings.HasPrefix(got, "no handler") {
			t.Errorf("no handler for %q", i)
		}
	}
}

// The question's option set and the enum must be the same set.
func TestQuestionOffersEveryIntent(t *testing.T) {
	declared := question.Options()
	if len(declared) != len(AllIntents) {
		t.Fatalf("the question offers %d options but %d intents are declared",
			len(declared), len(AllIntents))
	}
	seen := make(map[Intent]bool, len(declared))
	for _, o := range declared {
		seen[o] = true
	}
	for _, i := range AllIntents {
		if !seen[i] {
			t.Errorf("intent %q is declared but the question does not offer it", i)
		}
	}
}

// An answer naming an option nobody declared must be an error, not a silent
// fallthrough.
func TestExhaustiveCatchesDrift(t *testing.T) {
	ans := typesafe.ChoiceAnswerOf[Intent]{
		Choice:        "escalate_to_legal",
		Probabilities: map[Intent]float64{"escalate_to_legal": 1},
	}
	if err := typesafe.Exhaustive(ans, AllIntents...); err == nil {
		t.Error("an undeclared option should be reported")
	}
}
