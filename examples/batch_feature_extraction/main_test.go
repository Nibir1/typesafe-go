package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/examples/internal/harness"
)

func TestRun(t *testing.T) {
	client, _ := harness.Client(t, "testdata/cassettes/batch_feature_extraction.jsonl")

	var out bytes.Buffer
	if err := run(context.Background(), client, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	if strings.Contains(got, "FAILED") {
		t.Errorf("an item failed against the cassette:\n%s", got)
	}
	// One header plus one row per review, and the summary.
	for i := 1; i <= len(reviews); i++ {
		if !strings.Contains(got, "\n") {
			t.Fatal("no output")
		}
	}
	if !strings.Contains(got, "succeeded") {
		t.Errorf("no summary line:\n%s", got)
	}
	t.Log("\n" + got)
}
