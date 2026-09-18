//go:build integration

package typesafe_test

import (
	"context"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
)

// TestPhase2EndToEnd drives every typed primitive against the live API in one
// call, then reads each answer back through its typed accessor.
func TestPhase2EndToEnd(t *testing.T) {
	apiKey(t)
	c, err := typesafe.NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	req := &typesafe.SystemOneRequest{
		State: map[string]any{
			"ticket": map[string]any{
				"messages": []any{map[string]any{
					"text": "Our API has returned 500s for 20 minutes and we cannot process orders.",
				}},
			},
		},
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.Noul{
				Instructions: "Does `ticket.messages[0].text` convey urgency?",
				Criteria:     &typesafe.NoulCriteria{True: "Explicitly time-sensitive", False: "No urgency"},
			},
			"department": typesafe.Choice{
				Instructions: "Which team should handle this?",
				Criteria: typesafe.Options{
					"billing":   "Payments, invoicing, refunds",
					"technical": "Bugs, outages, integrations",
					"sales":     nil,
				},
			},
			"severity": typesafe.Score{
				Instructions: "How severe is the incident?",
				Criteria:     typesafe.Levels{"Cosmetic", "Degraded", "Outage"},
			},
		},
	}

	if w := req.CheckReferences(); len(w) != 0 {
		t.Errorf("valid references warned: %v", w)
	}
	if warnings, err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	} else if len(warnings) != 0 {
		t.Logf("warnings: %v", warnings)
	}

	resp, err := c.SystemOne(context.Background(), req)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	t.Logf("answered by %s, %d input tokens", resp.Model, resp.Usage.InputTokens)

	n, err := resp.Noul("is_urgent")
	if err != nil {
		t.Fatalf("Noul: %v", err)
	}
	t.Logf("is_urgent  noul=%.3f  bool(0.5)=%v", n.Noul, n.Bool(0.5))
	if _, ok := resp.Confidence("is_urgent"); ok {
		t.Error("a live noul reported a confidence; the contract says it has none")
	}

	ch, err := resp.Choice("department")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	t.Logf("department choice=%q confidence=%.3f margin=%.3f ranked=%v",
		ch.Choice, ch.Confidence, ch.Margin(), ch.Ranked())
	if len(ch.Probabilities) != 3 {
		t.Errorf("got %d probabilities, want one per option", len(ch.Probabilities))
	}

	s, err := resp.Score("severity")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	i, label := s.Nearest()
	t.Logf("severity score=%.3f nearest=%d(%v) atOrAbove(1)=%.3f levels=%v",
		s.Score, i, label, s.AtOrAbove(1), s.Levels())
	if s.NumLevels() != 3 {
		t.Errorf("legend has %d levels, want 3", s.NumLevels())
	}

	if _, err := resp.Choice("is_urgent"); err == nil {
		t.Error("a wrong accessor should error")
	}
}
