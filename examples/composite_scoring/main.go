// Command composite_scoring combines several signals into one auditable
// verdict.
//
//	go run ./composite_scoring
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

const post = `Make $5000/week from home! No experience needed.
DM me for details, limited spots. My cousin cleared 12k last month, total scam-free.
Serious inquiries only — losers need not apply.`

// One broad question — "should this be removed?" — gets one probability
// covering several unrelated propositions, and no threshold can split it back
// apart. Decompose into atomic signals and weigh them here, where the weights
// are reviewable, versionable and testable.
var questions = typesafe.Questions{
	"is_solicitation": typesafe.Noul{Instructions: "Is this post recruiting people into a money-making scheme?"},
	"is_unverifiable": typesafe.Noul{Instructions: "Does this post make income claims it does not support with evidence?"},
	"is_hostile":      typesafe.Noul{Instructions: "Does this post insult or demean a group of people?"},
	"is_urgent_push":  typesafe.Noul{Instructions: "Does this post pressure the reader to act immediately?"},
}

// The policy. Weights say what matters and by how much; thresholds say what to
// do about the total. Both live in code, under review, not in a prompt.
var policy = decision.Policy{
	Name: "community_moderation.v3",
	Weights: decision.Weights{
		"is_solicitation": 3,
		"is_unverifiable": 2,
		"is_hostile":      2,
		"is_urgent_push":  1,
	},
	// Normalize keeps the score in [0,1], so adding a ninth signal later does
	// not silently move every threshold.
	Normalize:   true,
	WarnAbove:   0.35,
	ReviewAbove: 0.55,
	BlockAbove:  0.80,
	// Fail loudly when a weighted question has no answer, rather than
	// scoring as though the signal were zero.
	OnMissing: decision.MissingIsError,
}

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	if err := policy.Validate(); err != nil {
		return err
	}

	resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
		State:     post,
		Questions: questions,
	})
	if err != nil {
		return err
	}

	// A response is already a decision.Source, so the policy reads it directly.
	result, err := policy.Evaluate(resp)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "%s: %s (score %.4f)\n\n", result.Policy, result.Verdict, result.Score)

	// The trace is the half that matters in an appeal. A verdict on its own is
	// an assertion; this is the arithmetic, and it is the same arithmetic for
	// every post.
	fmt.Fprintln(out, "contributions, largest first:")
	for _, t := range result.Trace.Terms {
		fmt.Fprintf(out, "  %-18s %.1f x %.3f = %.3f\n",
			t.QuestionID, t.Weight, t.Value, t.Contribution)
	}

	fmt.Fprintf(out, "\n-> %s\n", action(result.Verdict))
	return nil
}

func action(v decision.Verdict) string {
	switch v {
	case decision.Block:
		return "remove the post and notify the author"
	case decision.Review:
		return "hide pending moderator review"
	case decision.Warn:
		return "publish, flagged for the moderation dashboard"
	default:
		return "publish"
	}
}
