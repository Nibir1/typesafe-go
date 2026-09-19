package temporal_test

import (
	"log"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	typesafe "github.com/nibir1/typesafe-go"
	tstemporal "github.com/nibir1/typesafe-go/integrations/temporal"
)

// TriageWorkflow shows the arrangement this package exists to enforce: the API
// call happens in an Activity, never in workflow code.
//
// Workflow code is replayed from history after a restart or a deploy, so it
// must produce the same commands every time. An HTTP call cannot: the second
// run would make a second call and could get a different answer. An Activity's
// recorded result is replayed instead of re-running, which is what makes a
// model call safe here.
func TriageWorkflow(ctx workflow.Context, ticket string) (string, error) {
	// NewInput only marshals — no network, deterministic — so it is safe in
	// workflow code.
	in, err := tstemporal.NewInput(ticket, typesafe.Questions{
		"team": typesafe.Choice{
			Instructions: "Which team should handle this?",
			Criteria: typesafe.Options{
				"billing":   "Payments, invoicing, refunds",
				"technical": "Bugs, outages, integrations",
			},
		},
	})
	if err != nil {
		return "", err
	}

	resp, err := tstemporal.ExecuteSystemOne(ctx, in)
	if err != nil {
		return "", err
	}

	team, err := resp.Choice("team")
	if err != nil {
		return "", err
	}
	if team.Confidence < 0.7 {
		return "human_review", nil
	}
	return team.Choice, nil
}

// Compiled and type-checked, not executed: running it needs a Temporal cluster
// and a real key.
func Example() {
	c, err := client.Dial(client.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	// Temporal owns the retries, so the SDK must not add a second layer:
	// three SDK attempts inside three Temporal attempts is nine calls for one
	// logical request.
	ts, err := typesafe.NewClient(typesafe.WithRetryPolicy(typesafe.NoRetry()))
	if err != nil {
		log.Fatal(err)
	}

	w := worker.New(c, "triage", worker.Options{})
	tstemporal.Register(w, tstemporal.NewActivities(ts))
	w.RegisterWorkflow(TriageWorkflow)

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}
