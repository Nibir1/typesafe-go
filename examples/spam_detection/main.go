// Command spam_detection shows why a probability beats a boolean.
//
//	go run ./spam_detection
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"text/tabwriter"

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

var messages = []string{
	"WINNER!!! Claim your FREE iPhone now: bit.ly/3xK9p",
	"Hi Sarah, following up on the invoice we discussed last Tuesday.",
	"Quick question about your extended car warranty — act now!",
}

// Two thresholds, not one. Between them is the band where a human is cheaper
// than either mistake.
const (
	blockAbove = 0.90 // confident enough to delete without review
	flagAbove  = 0.60 // suspicious enough to quarantine
)

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "p(spam)\taction\tmessage")

	for _, msg := range messages {
		resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
			State: msg,
			Questions: typesafe.Questions{
				"is_spam": typesafe.Noul{
					Instructions: "Is this message unsolicited bulk or scam content?",
					Criteria: &typesafe.NoulCriteria{
						True: "Unsolicited promotion, phishing, or a scam",
						// Written as a presence, not an absence. A Noul whose
						// false describes "not spam" reads as a double
						// negative to the model and measurably degrades.
						False: "A genuine message from a real correspondent",
					},
				},
			},
		})
		if err != nil {
			return err
		}

		spam, err := resp.Noul("is_spam")
		if err != nil {
			return err
		}

		var action string
		switch {
		case spam.Noul >= blockAbove:
			action = "block"
		case spam.Noul >= flagAbove:
			action = "quarantine"
		default:
			action = "deliver"
		}

		fmt.Fprintf(w, "%.2f\t%s\t%.44s\n", spam.Noul, action, msg)
	}
	return w.Flush()
}
