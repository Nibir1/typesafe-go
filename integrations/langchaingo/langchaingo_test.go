package langchaingo_test

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/tools"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/cassette"
	tslangchain "github.com/nibir1/typesafe-go/integrations/langchaingo"
)

var update = flag.Bool("update", false, "re-record the cassette against the live API")

const cassettePath = "testdata/cassettes/classify.jsonl"

func questions() typesafe.Questions {
	return typesafe.Questions{
		"is_urgent": typesafe.Noul{
			Instructions: "Does this message convey urgency?",
			Criteria: &typesafe.NoulCriteria{
				True:  "The sender needs a response today",
				False: "The sender can wait",
			},
		},
		"team": typesafe.Choice{
			Instructions: "Which team should handle this?",
			Criteria: typesafe.Options{
				"billing":   "Payments, invoicing, refunds",
				"technical": "Bugs, outages, integrations",
				"sales":     "Pricing, upgrades, new accounts",
			},
		},
		"severity": typesafe.Score{
			Instructions: "How severe is the problem described here?",
			Criteria:     typesafe.Levels{"No impact", "Minor", "Blocking", "Unusable"},
		},
	}
}

const sampleTicket = "My card was declined twice on renewal and I was charged anyway. " +
	"The billing page also returns a 500 error."

func cassetteClient(t *testing.T) (*typesafe.Client, *http.Client) {
	t.Helper()
	hc := cassette.Open(t, cassettePath, cassette.Recording(*update),
		cassette.WithSecret(os.Getenv(typesafe.EnvAPIKey)))

	key := os.Getenv(typesafe.EnvAPIKey)
	if key == "" {
		key = "replay"
	}
	c, err := typesafe.NewClient(
		typesafe.WithAPIKey(key),
		typesafe.WithHTTPClient(hc),
		typesafe.WithDefaultModel("jev-latest"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, hc
}

// The exit criterion: a smoke test against a cassette, exercising the tool the
// way an agent would.
func TestClassifierAgainstACassette(t *testing.T) {
	client, hc := cassetteClient(t)

	classifier := tslangchain.NewClassifier(client, questions(),
		tslangchain.WithName("classify_support_ticket"),
		tslangchain.WithDescription("Classify a support ticket. Input: the ticket text."),
		tslangchain.WithModel("jev-latest"),
	)

	// It is usable as a langchaingo tool.
	var _ tools.Tool = classifier

	if classifier.Name() != "classify_support_ticket" {
		t.Errorf("Name = %q", classifier.Name())
	}
	if !strings.Contains(classifier.Description(), "ticket text") {
		t.Errorf("Description = %q", classifier.Description())
	}

	out, err := classifier.Call(context.Background(), sampleTicket)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	// An agent has to parse this, so it must be JSON with a predictable shape.
	var answers map[string]tslangchain.Answer
	if err := json.Unmarshal([]byte(out), &answers); err != nil {
		t.Fatalf("the tool returned something an agent cannot parse: %v\n%s", err, out)
	}
	if len(answers) != 3 {
		t.Fatalf("%d answers, want 3: %s", len(answers), out)
	}

	// A Noul has no confidence — its probability is the uncertainty — and the
	// field must be absent rather than zero, which would read as "completely
	// unsure".
	noul := answers["is_urgent"]
	if noul.Type != "noul" {
		t.Errorf("is_urgent type = %q", noul.Type)
	}
	if noul.Confidence != nil {
		t.Errorf("a Noul reported a confidence of %v; it has none", *noul.Confidence)
	}
	if p, ok := noul.Value.(float64); !ok || p < 0 || p > 1 {
		t.Errorf("is_urgent value = %v, want a probability", noul.Value)
	}

	choice := answers["team"]
	if choice.Type != "choice" {
		t.Errorf("team type = %q", choice.Type)
	}
	if choice.Confidence == nil {
		t.Error("a Choice must carry a confidence")
	}
	switch choice.Value {
	case "billing", "technical", "sales":
	default:
		t.Errorf("team = %v, which is outside the declared option set", choice.Value)
	}
	if len(choice.Distribution) != 3 {
		t.Errorf("distribution has %d entries, want one per option", len(choice.Distribution))
	}

	score := answers["severity"]
	if score.Type != "score" {
		t.Errorf("severity type = %q", score.Type)
	}
	if score.Confidence == nil {
		t.Error("a Score must carry a confidence")
	}

	// And the typed accessors are still reachable for callers who want them.
	resp, err := classifier.Response(context.Background(), sampleTicket)
	if err != nil {
		t.Fatalf("Response: %v", err)
	}
	if resp.Model == "" {
		t.Error("the response carries no model")
	}

	if !cassette.Recording(*update) {
		cassette.AssertAllPlayed(t, hc)
	}
}

func TestChainAgainstACassette(t *testing.T) {
	client, _ := cassetteClient(t)

	chain := tslangchain.NewChain(client, questions(), tslangchain.WithModel("jev-latest"))
	var _ chains.Chain = chain

	if got := chain.GetInputKeys(); len(got) != 1 || got[0] != "state" {
		t.Errorf("GetInputKeys = %v", got)
	}
	if got := chain.GetOutputKeys(); len(got) == 0 || got[0] != "answers" {
		t.Errorf("GetOutputKeys = %v", got)
	}
	if chain.GetMemory() == nil {
		t.Error("GetMemory returned nil; chains.Call would panic")
	}

	out, err := chain.Call(context.Background(), map[string]any{"state": sampleTicket})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	answers, ok := out["answers"].(map[string]tslangchain.Answer)
	if !ok {
		t.Fatalf("answers is %T, want map[string]Answer", out["answers"])
	}
	if len(answers) != 3 {
		t.Errorf("%d answers, want 3", len(answers))
	}
	if out["model"] == "" {
		t.Error("the chain did not report the model")
	}
	if n, _ := out["input_tokens"].(int); n <= 0 {
		t.Errorf("input_tokens = %v, want a positive count", out["input_tokens"])
	}
}

// --- failures, which do not need a cassette ----------------------------------

func TestEmptyInputIsRejected(t *testing.T) {
	c := tslangchain.NewClassifier(nil, questions())
	if _, err := c.Call(context.Background(), "   "); err == nil {
		t.Error("an empty input should be rejected before any request is made")
	}
}

func TestChainRejectsAMissingOrWrongInput(t *testing.T) {
	chain := tslangchain.NewChain(nil, questions())

	if _, err := chain.Call(context.Background(), map[string]any{}); err == nil {
		t.Error("a missing input key should be an error")
	} else if !strings.Contains(err.Error(), "state") {
		t.Errorf("the error should name the missing key, got: %v", err)
	}

	if _, err := chain.Call(context.Background(), map[string]any{"state": 42}); err == nil {
		t.Error("a non-string input should be an error")
	}
}

func TestGeneratedDescriptionIsStable(t *testing.T) {
	c := tslangchain.NewClassifier(nil, questions())
	first := c.Description()
	for i := 0; i < 20; i++ {
		if got := c.Description(); got != first {
			t.Fatalf("the generated description varies between calls:\n %s\n %s", first, got)
		}
	}
	// It must at least name every question, or an agent cannot tell what it
	// will get back.
	for _, id := range []string{"is_urgent", "team", "severity"} {
		if !strings.Contains(first, id) {
			t.Errorf("the generated description does not mention %q: %s", id, first)
		}
	}
}

func TestCustomKeys(t *testing.T) {
	chain := tslangchain.NewChain(nil, questions(), tslangchain.WithKeys("text", "result"))
	if got := chain.GetInputKeys(); got[0] != "text" {
		t.Errorf("GetInputKeys = %v, want [text]", got)
	}
	if got := chain.GetOutputKeys(); got[0] != "result" {
		t.Errorf("GetOutputKeys = %v, want result first", got)
	}
}
