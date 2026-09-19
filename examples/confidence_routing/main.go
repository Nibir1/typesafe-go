// Command confidence_routing shows the three-path pattern — act, check, refuse
// — and where low confidence actually comes from.
//
//	go run ./confidence_routing
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

var requests = []string{
	// One intent, nothing else fits.
	"I want a refund for the duplicate charge on my card.",
	// Vague — and answered confidently, because "unclear" is genuinely
	// correct. Vagueness does not lower confidence.
	"hey",
	// Two real intents at once. This is what lowers confidence.
	"it says error and also I think I paid twice but not sure, can you look",
}

// The same question, with and without a catch-all. Running both is the point
// of the example: the option set changes the confidence as much as the input
// does.
var (
	withCatchAll = typesafe.Choice{
		Instructions: "What is the customer asking for?",
		Criteria: typesafe.Options{
			"refund":           "They want money back for something already paid",
			"cancel":           "They want to end their subscription",
			"technical_help":   "Something is broken or erroring",
			"billing_question": "A question about charges, invoices or plans, with no refund asked for",
			"unclear":          "The request cannot be determined from this message",
		},
	}

	withoutCatchAll = typesafe.Choice{
		Instructions: withCatchAll.Instructions,
		Criteria: typesafe.Options{
			"refund":           "They want money back for something already paid",
			"cancel":           "They want to end their subscription",
			"technical_help":   "Something is broken or erroring",
			"billing_question": "A question about charges, invoices or plans, with no refund asked for",
		},
	}
)

// Above ActAbove, proceed. Below ConfirmAbove, do not guess. In between, act
// but verify — the path most code forgets to write.
var bands = decision.Bands{ActAbove: 0.90, ConfirmAbove: 0.50}

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	if err := bands.Validate(); err != nil {
		return err
	}

	for _, req := range requests {
		fmt.Fprintf(out, "%q\n", req)

		// Both question sets, same state, one request. Questions in one call
		// are evaluated in parallel and cost only their own tokens.
		resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
			State: req,
			Questions: typesafe.Questions{
				"with_catchall":    withCatchAll,
				"without_catchall": withoutCatchAll,
			},
		})
		if err != nil {
			return err
		}

		for _, id := range []string{"with_catchall", "without_catchall"} {
			intent, err := resp.Choice(id)
			if err != nil {
				return err
			}
			band := bands.Classify(intent.Confidence)
			fmt.Fprintf(out, "  %-17s %-17s conf %.2f  margin %.2f  -> %s\n",
				id, intent.Choice, intent.Confidence, intent.Margin(), band)
		}

		// Route on the question you would actually ship: the one with a
		// catch-all, so the model has somewhere to put an unanswerable input.
		intent, err := resp.Choice("with_catchall")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "  %s\n\n", act(intent, bands.Classify(intent.Confidence)))
	}
	return nil
}

func act(intent typesafe.ChoiceAnswer, band decision.Band) string {
	// The option matters as much as the band. A *confident* "unclear" is a
	// correct answer that still must not be dispatched as an intent: it means
	// ask the customer, not escalate to a colleague.
	if intent.Choice == "unclear" && band == decision.Act {
		return "-> asking the customer to say more"
	}
	switch band {
	case decision.Act:
		return fmt.Sprintf("-> executing %s", intent.Choice)
	case decision.Confirm:
		return fmt.Sprintf("-> executing %s, and asking the customer to confirm", intent.Choice)
	default:
		// Note what is not here: a guess. Code that branches on the winning
		// option alone cannot express this case, so it takes the automatic
		// path for an answer the model is telling it not to trust.
		return "-> routing to a person; the model is not sure enough"
	}
}
