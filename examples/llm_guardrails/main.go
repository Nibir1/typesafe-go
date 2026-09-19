// Command llm_guardrails checks a generative model's output before it reaches
// a user.
//
//	go run ./llm_guardrails
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

// What the user asked, and what a generative model produced. The guardrail
// judges the draft *against the question that was asked*, which is why both
// are in the state: "is this grounded?" is meaningless without the source.
type exchange struct {
	question string
	source   string
	draft    string
}

var exchanges = []exchange{
	{
		question: "What is your refund window?",
		source:   "Refunds are available within 30 days of purchase, minus any usage fees.",
		draft:    "You can request a refund within 30 days of purchase. Usage fees are deducted.",
	},
	{
		question: "What is your refund window?",
		source:   "Refunds are available within 30 days of purchase, minus any usage fees.",
		draft: "We offer a full 90-day money-back guarantee, no questions asked, " +
			"and we'll throw in a free month for the trouble.",
	},
}

var guardrails = typesafe.Questions{
	"is_grounded": typesafe.Noul{
		Instructions: "Is every factual claim in `draft` supported by `source`?",
		Criteria: &typesafe.NoulCriteria{
			True:  "Every claim traces to the source text",
			False: "At least one claim is not in the source",
		},
	},
	"answers_question": typesafe.Noul{
		Instructions: "Does `draft` answer the question in `question`?",
	},
	"makes_promise": typesafe.Noul{
		Instructions: "Does `draft` commit to something beyond what `source` states?",
	},
	"tone": typesafe.Score{
		Instructions: "Rate the tone of `draft`.",
		Criteria: typesafe.Levels{
			"Cold or dismissive",
			"Neutral and factual",
			"Warm and helpful",
			"Overfamiliar or salesy",
		},
	},
}

// A guardrail policy: any one of these failing is enough to hold the draft, so
// the weights are blunt and the block threshold is low.
var policy = decision.Policy{
	Name: "response_guardrail.v1",
	Weights: decision.Weights{
		"not_grounded":  3,
		"makes_promise": 2,
	},
	Normalize:   true,
	ReviewAbove: 0.30,
	BlockAbove:  0.60,
	OnMissing:   decision.MissingIsError,
}

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	for i, ex := range exchanges {
		// Structured state, not a flattened sentence. The questions refer to
		// `draft`, `source` and `question` by name, which is only possible
		// because the state has named fields.
		resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
			State: map[string]any{
				"question": ex.question,
				"source":   ex.source,
				"draft":    ex.draft,
			},
			Questions: guardrails,
		})
		if err != nil {
			return err
		}

		grounded, err := resp.Noul("is_grounded")
		if err != nil {
			return err
		}
		answers, err := resp.Noul("answers_question")
		if err != nil {
			return err
		}
		promise, err := resp.Noul("makes_promise")
		if err != nil {
			return err
		}
		tone, err := resp.Score("tone")
		if err != nil {
			return err
		}

		// The policy weighs risk, so it needs "not grounded" rather than
		// "grounded". Inverting here keeps the question written as a
		// presence, which is how a Noul performs best.
		signals := decision.Nouls{
			"not_grounded":  decision.Not(grounded.Noul),
			"makes_promise": promise.Noul,
		}
		result, err := policy.Evaluate(signals)
		if err != nil {
			return err
		}

		fmt.Fprintf(out, "draft %d\n", i+1)
		fmt.Fprintf(out, "  grounded    %.2f\n", grounded.Noul)
		fmt.Fprintf(out, "  answers     %.2f\n", answers.Noul)
		fmt.Fprintf(out, "  overpromise %.2f\n", promise.Noul)
		fmt.Fprintf(out, "  tone        %.2f (%s)\n", tone.Score, nearestLevel(tone))
		fmt.Fprintf(out, "  verdict     %s (risk %.3f)\n", result.Verdict, result.Score)

		switch {
		case result.Verdict >= decision.Block:
			fmt.Fprintln(out, "  -> do not send; regenerate")
		case result.Verdict >= decision.Review:
			fmt.Fprintln(out, "  -> hold for a human")
		case answers.Noul < 0.5:
			// Grounded and honest, but it did not answer the question.
			fmt.Fprintln(out, "  -> grounded but off-topic; regenerate")
		default:
			fmt.Fprintln(out, "  -> send")
		}
		fmt.Fprintln(out)
	}
	return nil
}

func nearestLevel(a typesafe.ScoreAnswer) string {
	_, desc := a.Nearest()
	if s, ok := desc.(string); ok {
		return s
	}
	return "unknown"
}
