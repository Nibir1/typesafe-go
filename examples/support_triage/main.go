// Command support_triage asks everything about one ticket in a single call.
//
//	go run ./support_triage
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	typesafe "github.com/nibir1/typesafe-go"
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

const ticket = `Subject: Charged twice and the dashboard is down

My subscription renewal was declined twice today and I was charged anyway.
The billing page also returns a 500 error, so I can't even check my invoices.
This is the third time this month.`

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	// Every question about one state belongs in a single request. They are
	// evaluated in parallel server-side and cost only their own tokens, while
	// a second *state* costs a whole request — the state is by far the larger
	// part of the bill.
	resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
		State: ticket,
		Questions: typesafe.Questions{
			"team": typesafe.Choice{
				Instructions: "Which team should handle this ticket?",
				Criteria: typesafe.Options{
					"billing":   "Payments, invoicing, refunds, charges",
					"technical": "Bugs, outages, errors, integrations",
					"sales":     "Pricing, upgrades, new accounts",
					"other":     nil, // a catch-all, read by its name alone
				},
			},
			"severity": typesafe.Score{
				Instructions: "How severe is the problem described here?",
				// Order is meaning: a level's position is its score.
				Criteria: typesafe.Levels{
					"No impact on the customer",
					"Annoying but there is a workaround",
					"One workflow is blocked",
					"The product is unusable",
				},
			},
			"is_repeat": typesafe.Noul{
				Instructions: "Does the sender say this has happened before?",
				Criteria: &typesafe.NoulCriteria{
					True:  "They mention a previous occurrence",
					False: "No previous occurrence is mentioned",
				},
			},
		},
	})
	if err != nil {
		return err
	}

	team, err := resp.Choice("team")
	if err != nil {
		return err
	}
	severity, err := resp.Score("severity")
	if err != nil {
		return err
	}
	repeat, err := resp.Noul("is_repeat")
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "team:     %-10s confidence %.2f (margin %.2f)\n",
		team.Choice, team.Confidence, team.Margin())
	fmt.Fprintf(out, "severity: %.2f      confidence %.2f\n", severity.Score, severity.Confidence)
	fmt.Fprintf(out, "repeat:   %.2f\n\n", repeat.Noul)

	// Threshold on the distribution, not on the interpolated score. TypeSafe
	// documents that score levels are weakly calibrated numerically, so
	// "is it at least Blocking?" is sound where "is it 2.4?" is not.
	blocking := severity.AtOrAbove(2)
	fmt.Fprintf(out, "P(at least blocking) = %.2f\n", blocking)

	switch {
	case blocking > 0.8 && repeat.Noul > 0.5:
		fmt.Fprintln(out, "-> page the on-call for", team.Choice)
	case blocking > 0.8:
		fmt.Fprintln(out, "-> priority queue for", team.Choice)
	default:
		fmt.Fprintln(out, "-> normal queue for", team.Choice)
	}
	return nil
}
