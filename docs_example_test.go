package typesafe_test

// The code in docs/MANUAL.md and README.md, verbatim.
//
// It is an Example with no // Output: comment, so the toolchain compiles and
// type-checks it but never runs it — running would need a key and a network.
// That is the whole point: a signature change makes the documentation fail to
// build instead of quietly making it wrong, which is the failure mode prose
// has and code does not.
//
// When you change a snippet in the manual, change it here too. When this stops
// compiling, the manual is already wrong.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/cassette"
	"github.com/nibir1/typesafe-go/decision"
)

type docTopic string

const (
	docTopicBilling   docTopic = "billing"
	docTopicTechnical docTopic = "technical"
)

func docEscalate()    {}
func docRoute(string) {}
func docConfirm()     {}
func docHandOff()     {}

func Example_documentation() {
	ctx := context.Background()

	client, err := typesafe.NewClient(
		typesafe.WithTimeout(60*time.Second),
		typesafe.WithDefaultModel("jev-1.13.0"),
		typesafe.WithContextLimitCheck(false),
		typesafe.WithRetryPolicy(typesafe.RetryPolicy{
			MaxRetries:        2,
			BackoffInitial:    500 * time.Millisecond,
			BackoffMax:        5 * time.Second,
			BackoffJitter:     0.25,
			RespectRetryAfter: true,
			MaxRetryAfter:     30 * time.Second,
			Timeout:           30 * time.Second,
		}),
		typesafe.WithCircuitBreaker(&typesafe.CircuitBreaker{
			Threshold: 5, OpenFor: 30 * time.Second, HalfOpenProbes: 1,
		}),
		typesafe.WithBudget(typesafe.NewBudget(
			typesafe.MaxRequestsPerMinute(600),
			typesafe.MaxTotalTokens(5_000_000),
		)),
	)
	if err != nil {
		log.Fatal(err)
	}

	req := &typesafe.SystemOneRequest{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: typesafe.Questions{
			"is_urgent": typesafe.Noul{
				Instructions: "Does this message convey urgency?",
				Criteria: &typesafe.NoulCriteria{
					True:  "The sender needs a response today",
					False: "The sender can wait",
				},
			},
			"team": typesafe.Choice{
				Instructions: "Which team should handle this ticket?",
				Criteria: typesafe.Options{
					"billing":   "Payments, invoicing, refunds",
					"technical": "Bugs, outages, integrations",
					"other":     nil,
				},
			},
			"severity": typesafe.Score{
				Instructions: "How severe is the problem described here?",
				Criteria: typesafe.Levels{
					"No impact on the customer",
					"Annoying but there is a workaround",
					"One workflow is blocked",
					"The product is unusable",
				},
			},
		},
	}

	est := req.EstimateTokens()
	if err := est.Err(); err != nil {
		log.Fatal(err)
	}

	resp, err := client.SystemOne(ctx, req)
	if err != nil {
		var rl *typesafe.RateLimitError
		if errors.As(err, &rl) {
			time.Sleep(rl.RetryAfter)
		}
		if errors.Is(err, typesafe.ErrRateLimit) {
			return
		}
		log.Fatal(err)
	}

	urgent, err := resp.Noul("is_urgent")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("urgency: %.2f\n", urgent.Noul)
	if urgent.Bool(0.8) {
		docEscalate()
	}

	team, err := resp.Choice("team")
	if err != nil {
		log.Fatal(err)
	}
	severity, err := resp.Score("severity")
	if err != nil {
		log.Fatal(err)
	}
	if severity.AtOrAbove(2) > 0.8 {
		docEscalate()
	}

	bands := decision.Bands{ActAbove: 0.90, ConfirmAbove: 0.50}
	switch bands.Classify(team.Confidence) {
	case decision.Act:
		docRoute(team.Choice)
	case decision.Confirm:
		docRoute(team.Choice)
		docConfirm()
	case decision.Escalate:
		docHandOff()
	}

	answer, err := resp.Answer("is_urgent")
	if err != nil {
		log.Fatal(err)
	}
	switch a := answer.(type) {
	case typesafe.NoulAnswer:
		_ = a.Noul
	case typesafe.ChoiceAnswer:
		_, _ = a.Choice, a.Confidence
	case typesafe.ScoreAnswer:
		_, _ = a.Score, a.Confidence
	}

	policy := decision.Policy{
		Name: "moderation.v3",
		Weights: decision.Weights{
			"is_solicitation": 3,
			"is_unverifiable": 2,
			"is_hostile":      2,
		},
		Normalize:   true,
		ReviewAbove: 0.55,
		BlockAbove:  0.80,
		OnMissing:   decision.MissingIsError,
	}
	_ = policy

	q := typesafe.TypedChoice[docTopic]("Which team?",
		typesafe.OptionOf(docTopicBilling, "Payments, invoicing, refunds"),
		typesafe.OptionOf(docTopicTechnical, "Bugs, outages, integrations"),
	)
	ans, err := q.Answer(resp, "team")
	if err == nil {
		switch ans.Choice {
		case docTopicBilling:
		case docTopicTechnical:
		}
	}

	states := []any{"a", "b"}
	result := client.SystemOneBatch(ctx, states, req.Questions,
		typesafe.WithConcurrency(16))
	for i, item := range result.Items {
		if item.Err != nil {
			continue
		}
		_ = i
	}

	replayClient, err := typesafe.NewClient(
		typesafe.WithAPIKey("test"),
		typesafe.WithHTTPClient(cassette.MustReplay("testdata/cassettes/triage.jsonl")),
	)
	_, _ = replayClient, err
}
