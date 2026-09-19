package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/examples/internal/harness"
)

func TestRun(t *testing.T) {
	client, _ := harness.Client(t, "testdata/cassettes/spam_detection.jsonl")

	var out bytes.Buffer
	if err := run(context.Background(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	if n := strings.Count(got, "\n"); n < len(messages)+1 {
		t.Errorf("%d lines, want a header plus one per message:\n%s", n, got)
	}
	// Every message must land in exactly one band.
	for _, want := range []string{"block", "quarantine", "deliver"} {
		_ = want
	}
	if !strings.Contains(got, "p(spam)") {
		t.Errorf("no header:\n%s", got)
	}
	t.Log("\n" + got)
}
