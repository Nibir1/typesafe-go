package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/examples/internal/harness"
)

func TestRun(t *testing.T) {
	client, _ := harness.Client(t, "testdata/cassettes/quickstart.jsonl")

	var out bytes.Buffer
	if err := run(context.Background(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "urgency:") {
		t.Errorf("no probability in the output:\n%s", got)
	}
	if !strings.Contains(got, "->") {
		t.Errorf("no decision in the output:\n%s", got)
	}
	t.Log("\n" + got)
}
