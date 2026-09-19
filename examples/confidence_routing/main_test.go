package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/examples/internal/harness"
)

func TestRun(t *testing.T) {
	client, _ := harness.Client(t, "testdata/cassettes/confidence_routing.jsonl")

	var out bytes.Buffer
	if err := run(context.Background(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "conf ") || !strings.Contains(got, "margin ") {
		t.Errorf("output is missing the confidence or margin:\n%s", got)
	}
	// The confident-but-unclear case is a distinct outcome from escalation,
	// and the example exists partly to separate them.
	if !strings.Contains(got, "asking the customer to say more") {
		t.Errorf("the confident-unclear path was not exercised:\n%s", got)
	}
	// Both question sets must be reported for every request, or the
	// comparison the example is built on is not visible.
	if a, b := strings.Count(got, "with_catchall"), strings.Count(got, "without_catchall"); a != b {
		t.Errorf("the two question sets are not reported symmetrically (%d vs %d):\n%s", a, b, got)
	}
	t.Log("\n" + got)
}
