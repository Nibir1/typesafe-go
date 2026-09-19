package langchaingo_test

import (
	"context"
	"fmt"
	"log"

	"github.com/tmc/langchaingo/tools"

	typesafe "github.com/nibir1/typesafe-go"
	tslangchain "github.com/nibir1/typesafe-go/integrations/langchaingo"
)

// Give an agent a classification tool whose answers cannot be a value nobody
// declared.
//
// Compiled and type-checked, not executed: running it needs a real key.
func Example() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}

	classifier := tslangchain.NewClassifier(client, typesafe.Questions{
		"team": typesafe.Choice{
			Instructions: "Which team should handle this ticket?",
			Criteria: typesafe.Options{
				"billing":   "Payments, invoicing, refunds",
				"technical": "Bugs, outages, integrations",
				"other":     nil,
			},
		},
		"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
	},
		tslangchain.WithName("classify_support_ticket"),
		// The description is the only thing telling the model when to reach
		// for this tool, so say what the input should be.
		tslangchain.WithDescription(
			"Classify a support ticket. Input: the full ticket text. "+
				"Returns the owning team and whether it is urgent."),
	)

	// It is an ordinary langchaingo tool, so it goes straight into an agent.
	var agentTools []tools.Tool
	agentTools = append(agentTools, classifier)
	_ = agentTools

	out, err := classifier.Call(context.Background(),
		"My card was declined twice and I was charged anyway.")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out)
}

// The same classification as a chain, for a pipeline rather than an agent.
func ExampleChain() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}

	chain := tslangchain.NewChain(client, typesafe.Questions{
		"is_spam": typesafe.Noul{Instructions: "Is this spam?"},
	})

	out, err := chain.Call(context.Background(), map[string]any{
		"state": "Buy cheap watches now!!!",
	})
	if err != nil {
		log.Fatal(err)
	}

	answers := out["answers"].(map[string]tslangchain.Answer)
	fmt.Printf("spam probability: %v\n", answers["is_spam"].Value)
}
