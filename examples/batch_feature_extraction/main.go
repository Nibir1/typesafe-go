// Command batch_feature_extraction runs the same questions over many states.
//
//	go run ./batch_feature_extraction
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

// A corpus. In production this is a database cursor, and there are a million
// of them rather than five.
var reviews = []any{
	"Shipping took three weeks and the box arrived crushed. The product itself is fine.",
	"Absolutely brilliant. Third one I've bought. Would recommend to anyone.",
	"Does what it says. Nothing special.",
	"Stopped working after two days and support has not replied to three emails.",
	"Good value but the instructions are translated badly and barely readable.",
}

// One question set, asked of every review. Adding a question here costs its own
// tokens once per review; adding a review costs a whole request.
var questions = typesafe.Questions{
	"sentiment": typesafe.Score{
		Instructions: "Rate the overall sentiment of this review.",
		Criteria: typesafe.Levels{
			"Very negative", "Negative", "Neutral", "Positive", "Very positive",
		},
	},
	"mentions_shipping": typesafe.Noul{Instructions: "Does this review mention delivery or shipping?"},
	"mentions_support":  typesafe.Noul{Instructions: "Does this review mention customer support?"},
	"is_defect":         typesafe.Noul{Instructions: "Does this review describe the product failing or breaking?"},
}

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	// The batch API batches states, never questions: Jev ingests the state
	// once and evaluates every question against it in parallel, so a second
	// question is nearly free while a second state is a whole request.
	result := client.SystemOneBatch(ctx, reviews, questions,
		typesafe.WithConcurrency(4),
	)

	// One failure never aborts the batch, so check the summary and keep the
	// results that did come back.
	if err := result.Err(); err != nil {
		fmt.Fprintf(out, "partial failure: %v\n\n", err)
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tsentiment\tship\tsupport\tdefect\treview")

	for i, item := range result.Items {
		if item.Err != nil {
			fmt.Fprintf(w, "%d\tFAILED\t\t\t\t%v\n", i+1, item.Err)
			continue
		}

		sentiment, err := item.Response.Score("sentiment")
		if err != nil {
			return err
		}
		shipping, err := item.Response.Noul("mentions_shipping")
		if err != nil {
			return err
		}
		support, err := item.Response.Noul("mentions_support")
		if err != nil {
			return err
		}
		defect, err := item.Response.Noul("is_defect")
		if err != nil {
			return err
		}

		text, _ := item.State.(string)
		fmt.Fprintf(w, "%d\t%.2f\t%.2f\t%.2f\t%.2f\t%.38s\n",
			i+1, sentiment.Score, shipping.Noul, support.Noul, defect.Noul, text)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%d succeeded, %d failed, %d input tokens, %s\n",
		result.Succeeded, result.Failed, result.Usage.InputTokens,
		result.Duration.Round(1e6))
	return nil
}
