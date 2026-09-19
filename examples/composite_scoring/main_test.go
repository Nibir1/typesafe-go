package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/examples/internal/harness"
)

func TestRun(t *testing.T) {
	client, _ := harness.Client(t, "testdata/cassettes/composite_scoring.jsonl")

	var out bytes.Buffer
	if err := run(context.Background(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	for _, want := range []string{"community_moderation.v3", "contributions", "->"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	// Every weighted question must appear in the trace, or the verdict is not
	// fully explained.
	for id := range questions {
		if !strings.Contains(got, id) {
			t.Errorf("the trace does not account for %q:\n%s", id, got)
		}
	}
	t.Log("\n" + got)
}

// The policy has to be internally consistent before it decides anything.
func TestPolicyIsValid(t *testing.T) {
	if err := policy.Validate(); err != nil {
		t.Fatalf("policy: %v", err)
	}
	for id := range policy.Weights {
		if _, ok := questions[id]; !ok {
			t.Errorf("the policy weighs %q but no such question is asked", id)
		}
	}
}
