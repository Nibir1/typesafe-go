// Command intent_routing dispatches to handlers from a typed Choice.
//
//	go run ./intent_routing
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/decision"
)

func main() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	if err := run(context.Background(), client, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// Intent is the option set, as a named type. The compiler then checks both
// ends: the question cannot offer an option the switch has no arm for, and a
// typo in an arm does not build.
type Intent string

const (
	IntentRefund    Intent = "refund"
	IntentCancel    Intent = "cancel"
	IntentTechnical Intent = "technical"
	IntentBilling   Intent = "billing_question"
	IntentUnclear   Intent = "unclear"
)

// AllIntents is what Exhaustive checks an answer against.
var AllIntents = []Intent{
	IntentRefund, IntentCancel, IntentTechnical, IntentBilling, IntentUnclear,
}

// The question, built from the same enum. There is one source of truth for the
// option set, so the question and the switch cannot drift apart.
var question = typesafe.TypedChoice[Intent](
	"What is the customer asking for?",
	typesafe.OptionOf(IntentRefund, "They want money back for something already paid"),
	typesafe.OptionOf(IntentCancel, "They want to end their subscription"),
	typesafe.OptionOf(IntentTechnical, "Something is broken or erroring"),
	typesafe.OptionOf(IntentBilling, "A question about charges, invoices or plans, with no refund asked for"),
	typesafe.OptionOf(IntentUnclear, "The request cannot be determined from this message"),
)

var bands = decision.Bands{ActAbove: 0.80, ConfirmAbove: 0.55}

var messages = []string{
	"I was charged twice for January, please refund the second one.",
	"How do I add a second seat to my plan?",
	"it's broken again",
}

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	for _, msg := range messages {
		resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
			State:     msg,
			Questions: typesafe.Questions{"intent": question},
		})
		if err != nil {
			return err
		}

		// Answer decodes into Intent and checks the result against the
		// declared set. An option the question never offered can only mean
		// the request and the enum drifted apart, and a type switch would
		// handle that by falling through to no branch at all.
		intent, err := question.Answer(resp, "intent")
		if err != nil {
			return err
		}
		if err := typesafe.Exhaustive(intent, AllIntents...); err != nil {
			return err
		}

		fmt.Fprintf(out, "%-46.46s -> %-16s %.2f %s\n",
			msg, intent.Choice, intent.Confidence, bands.Classify(intent.Confidence))

		if bands.Classify(intent.Confidence) == decision.Escalate {
			fmt.Fprintln(out, "     (not confident enough to dispatch; a person takes it)")
			continue
		}
		fmt.Fprintf(out, "     %s\n", dispatch(intent.Choice))
	}
	return nil
}

// dispatch is exhaustive over Intent. Add a constant without adding an arm and
// the default catches it at runtime — but the option set and this switch are
// built from the same declaration, so they cannot silently disagree.
func dispatch(i Intent) string {
	switch i {
	case IntentRefund:
		return "opening a refund case"
	case IntentCancel:
		return "starting the cancellation flow"
	case IntentTechnical:
		return "creating an engineering ticket"
	case IntentBilling:
		return "answering from the billing FAQ"
	case IntentUnclear:
		return "asking the customer to say more"
	default:
		return fmt.Sprintf("no handler for %q", i)
	}
}
