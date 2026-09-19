package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/examples/internal/harness"
)

func TestRun(t *testing.T) {
	client, _ := harness.Client(t, "testdata/cassettes/tool_call_verification.jsonl")

	var out bytes.Buffer
	if err := run(context.Background(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	for _, want := range []string{"matches", "broader", "blast", "->"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	// The example only earns its place if the bad proposals are caught.
	if strings.Count(got, "-> execute") == len(proposals) {
		t.Errorf("every proposal was approved; the check is not discriminating:\n%s", got)
	}
	t.Log("\n" + got)
}
