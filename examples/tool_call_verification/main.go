// Command tool_call_verification checks that a proposed tool call matches what
// the user actually asked for.
//
//	go run ./tool_call_verification
package main

import (
	"context"
	"encoding/json"
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

// toolCall is what an agent proposed doing.
type toolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"arguments"`
}

type proposal struct {
	userSaid string
	call     toolCall
}

var proposals = []proposal{
	{
		userSaid: "Cancel my order #4471, I ordered the wrong size.",
		call: toolCall{
			Name: "cancel_order",
			Args: map[string]any{"order_id": "4471", "reason": "wrong_size"},
		},
	},
	{
		// The user asked about one order. The proposed call touches every
		// order they have. Both are "cancel_order"; only the arguments give
		// it away, and only against what the user said.
		userSaid: "Cancel my order #4471, I ordered the wrong size.",
		call: toolCall{
			Name: "cancel_order",
			Args: map[string]any{"order_id": "*", "reason": "wrong_size"},
		},
	},
	{
		userSaid: "What's the status of my order?",
		call: toolCall{
			Name: "refund_order",
			Args: map[string]any{"order_id": "4471", "amount": 240.00},
		},
	},
}

var checks = typesafe.Questions{
	"matches_request": typesafe.Noul{
		Instructions: "Does the action in `proposed_call` do what the user asked for in `user_said`?",
		Criteria: &typesafe.NoulCriteria{
			True:  "The action and its arguments follow from what the user asked",
			False: "The action or its arguments go beyond or differ from the request",
		},
	},
	"is_broader": typesafe.Noul{
		Instructions: "Does `proposed_call` affect more than the user asked about?",
	},
	"blast_radius": typesafe.Score{
		Instructions: "How much would this action affect if it is wrong?",
		Criteria: typesafe.Levels{
			"Reads data, changes nothing",
			"Changes one record the user owns",
			"Changes several records or moves money",
			"Irreversible, or affects other people",
		},
	},
}

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	for i, p := range proposals {
		callJSON, err := json.Marshal(p.call)
		if err != nil {
			return err
		}

		resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
			State: map[string]any{
				"user_said":     p.userSaid,
				"proposed_call": json.RawMessage(callJSON),
			},
			Questions: checks,
		})
		if err != nil {
			return err
		}

		matches, err := resp.Noul("matches_request")
		if err != nil {
			return err
		}
		broader, err := resp.Noul("is_broader")
		if err != nil {
			return err
		}
		radius, err := resp.Score("blast_radius")
		if err != nil {
			return err
		}

		fmt.Fprintf(out, "%d. %s(%v)\n", i+1, p.call.Name, p.call.Args)
		fmt.Fprintf(out, "   matches %.2f  broader %.2f  blast %.2f\n",
			matches.Noul, broader.Noul, radius.Score)

		// The gate scales with the consequence. A read-only call needs less
		// certainty than one that moves money, and the same threshold for
		// both is either too strict to be useful or too loose to be safe.
		irreversible := radius.AtOrAbove(2)
		required := 0.70 + 0.25*irreversible

		switch {
		case broader.Noul > 0.5:
			fmt.Fprintln(out, "   -> refuse: wider than the request")
		case matches.Noul < required:
			fmt.Fprintf(out, "   -> confirm with the user (needed %.2f, got %.2f)\n",
				required, matches.Noul)
		default:
			fmt.Fprintln(out, "   -> execute")
		}
		fmt.Fprintln(out)
	}
	return nil
}
