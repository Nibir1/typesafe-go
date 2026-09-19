package mcp_test

import (
	"context"
	"log"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/decision"
	tsmcp "github.com/nibir1/typesafe-go/integrations/mcp"
)

// Serve TypeSafe over MCP with one policy the agent can evaluate but cannot
// alter.
//
// Compiled and type-checked, not executed: running it serves on stdio and
// needs a real key.
func Example() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}

	srv := tsmcp.NewServer(client, tsmcp.Options{
		// With systemone disabled the agent can only evaluate the policies
		// defined here — it cannot ask arbitrary questions.
		DisableSystemOne: true,
	})

	// The questions, the weights and the thresholds live on the server. The
	// agent names the policy and supplies text; it never sees how the
	// decision is made, and cannot be talked out of it by the text it judges.
	err = srv.AddPolicy("moderation", tsmcp.PolicySpec{
		Policy: decision.Policy{
			Name:        "moderation",
			Weights:     decision.Weights{"is_spam": 1, "is_abusive": 2},
			Normalize:   true,
			ReviewAbove: 0.5,
			BlockAbove:  0.9,
		},
		Questions: typesafe.Questions{
			"is_spam":    typesafe.Noul{Instructions: "Is this message spam?"},
			"is_abusive": typesafe.Noul{Instructions: "Is this message abusive towards a person?"},
		},
		Description: "Moderate user-submitted text. A block verdict means do not publish.",
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := srv.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
