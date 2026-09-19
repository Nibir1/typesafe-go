// Command quickstart asks one yes/no question and acts on the probability.
//
//	export TYPESAFE_API_KEY=...
//	go run ./quickstart
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

const message = "Help! My payouts have been failing for 3 days and nobody has replied."

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
		State: message,
		Questions: typesafe.Questions{
			"is_urgent": typesafe.Noul{
				Instructions: "Does this message convey urgency?",
				// Say what a yes and a no mean. TypeSafe documents that a Noul
				// whose true describes a "no" performs measurably worse, and
				// nothing in the resulting probability reveals the mistake.
				Criteria: &typesafe.NoulCriteria{
					True:  "The sender needs a response today",
					False: "The sender can wait",
				},
			},
		},
	})
	if err != nil {
		return err
	}

	urgent, err := resp.Noul("is_urgent")
	if err != nil {
		return err
	}

	// A Noul answer is a probability, not a boolean. The threshold is yours:
	// it is the point where you would rather be wrong one way than the other.
	fmt.Fprintf(out, "urgency: %.2f\n", urgent.Noul)
	if urgent.Bool(0.8) {
		fmt.Fprintln(out, "-> escalate")
	} else {
		fmt.Fprintln(out, "-> normal queue")
	}
	return nil
}
