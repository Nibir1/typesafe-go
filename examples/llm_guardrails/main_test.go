package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/examples/internal/harness"
)

func TestRun(t *testing.T) {
	client, _ := harness.Client(t, "testdata/cassettes/llm_guardrails.jsonl")

	var out bytes.Buffer
	if err := run(context.Background(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	for _, want := range []string{"grounded", "overpromise", "verdict", "->"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	// The example is only worth having if the two drafts land differently.
	if strings.Count(got, "-> send") == len(exchanges) {
		t.Errorf("every draft passed; the guardrail is not discriminating:\n%s", got)
	}
	t.Log("\n" + got)
}

func TestPolicyIsValid(t *testing.T) {
	if err := policy.Validate(); err != nil {
		t.Fatalf("policy: %v", err)
	}
}
