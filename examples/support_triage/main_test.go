package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/examples/internal/harness"
)

func TestRun(t *testing.T) {
	client, _ := harness.Client(t, "testdata/cassettes/support_triage.jsonl")

	var out bytes.Buffer
	if err := run(context.Background(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	for _, want := range []string{"team:", "severity:", "repeat:", "P(at least blocking)", "->"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	t.Log("\n" + got)
}
