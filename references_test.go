package typesafe_test

import (
	"strings"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
)

// supportState mirrors the nested state from the documentation's worked
// example, and from golden fixture 08.
func supportState() map[string]any {
	return map[string]any{
		"ticket": map[string]any{
			"subject": "Duplicate charge",
			"messages": []any{
				map[string]any{"from": "customer", "text": "I was charged twice for order A-104."},
				map[string]any{"from": "support", "text": "We are checking the charges."},
			},
		},
		"order": map[string]any{
			"id": "A-104",
			"charges": []any{
				map[string]any{"amount_usd": 49, "status": "captured"},
				map[string]any{"amount_usd": 49, "status": "captured"},
			},
		},
		"refund_policy": "Duplicate charges are eligible for a refund.",
	}
}

func refReq(instructions any) *typesafe.SystemOneRequest {
	return &typesafe.SystemOneRequest{
		State:     supportState(),
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{Instructions: instructions}},
	}
}

// TestValidReferencesProduceNoWarnings: the whole feature is worthless if it
// cries wolf on correct paths.
func TestValidReferencesProduceNoWarnings(t *testing.T) {
	valid := []string{
		"Does `ticket.messages[0].text` request a refund?",
		"Does `refund_policy` support it?",
		"Consider `order.charges[1].status` and `order.id`.",
		"Look at `ticket.subject`.",
		"Check `order.charges[0].amount_usd` against `refund_policy`.",
	}
	for _, instr := range valid {
		t.Run(instr, func(t *testing.T) {
			if w := refReq(instr).CheckReferences(); len(w) != 0 {
				t.Errorf("valid references warned: %v", w)
			}
		})
	}
}

// TestTypoIsCaught is the payoff. A misspelled path is silent at runtime: the
// model sees a path naming nothing, answers anyway, and the answer is quietly
// worse. Nothing in the response says so.
func TestTypoIsCaught(t *testing.T) {
	cases := map[string]struct{ instr, wantIn string }{
		"misspelled leaf":      {"Does `ticket.messages[0].mesage` request a refund?", "has no key"},
		"index out of range":   {"Check `ticket.messages[5].text`.", "out of range"},
		"indexing an object":   {"Check `ticket[0]`.", "not an array"},
		"keying into a string": {"Check `refund_policy.text`.", "not an object"},
		"keying into an array": {"Check `order.charges.status`.", "not an object"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := refReq(tc.instr).CheckReferences()
			if len(w) == 0 {
				t.Fatalf("no warning for %q", tc.instr)
			}
			if !strings.Contains(w[0].Reason, tc.wantIn) {
				t.Errorf("Reason = %q, want it to contain %q", w[0].Reason, tc.wantIn)
			}
			if w[0].QuestionID != "q" {
				t.Errorf("QuestionID = %q", w[0].QuestionID)
			}
			// The message must be actionable on its own.
			s := w[0].String()
			if !strings.Contains(s, "q") || !strings.Contains(s, "does not resolve") {
				t.Errorf("String() is not actionable: %q", s)
			}
		})
	}
}

// TestReasonNamesWhereTheWalkStopped: "has no key" is only useful if it says
// which object lacked it.
func TestReasonNamesWhereTheWalkStopped(t *testing.T) {
	w := refReq("Check `ticket.messages[0].mesage`.").CheckReferences()
	if len(w) != 1 {
		t.Fatalf("got %d warnings, want 1", len(w))
	}
	if !strings.Contains(w[0].Reason, "ticket.messages[0]") {
		t.Errorf("Reason should name the containing object, got: %q", w[0].Reason)
	}
	if !strings.Contains(w[0].Reason, "mesage") {
		t.Errorf("Reason should name the missing key, got: %q", w[0].Reason)
	}
}

// TestReferencesInCriteriaAndStructuredInstructions: paths are not only
// written in a plain instructions string.
func TestReferencesInStructuredFields(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State: supportState(),
		Questions: map[string]typesafe.Question{
			"structured": typesafe.Noul{Instructions: map[string]any{
				"question": "Does `ticket.messages[0].nope` matter?",
			}},
			"in_criteria": typesafe.Choice{
				Instructions: "Pick one.",
				Criteria: typesafe.Options{
					"a": "when `order.missing_field` is set",
					"b": nil,
				},
			},
			"in_levels": typesafe.Score{
				Instructions: "Rate it.",
				Criteria:     typesafe.Levels{"low", "high if `ticket.absent` is true"},
			},
		},
	}
	w := req.CheckReferences()
	if len(w) != 3 {
		t.Fatalf("got %d warnings, want 3: %v", len(w), w)
	}
	// Deterministic order by question id.
	if w[0].QuestionID != "in_criteria" || w[1].QuestionID != "in_levels" || w[2].QuestionID != "structured" {
		t.Errorf("warnings are not ordered by question id: %v", w)
	}
}

// TestNonPathBackticksAreIgnored: backticks are also ordinary emphasis, and a
// checker that warns about prose is one nobody leaves enabled.
func TestNonPathBackticksAreIgnored(t *testing.T) {
	quiet := []string{
		// Bare identifiers: emphasis, or a key of a structured instruction
		// object rather than of the state. TypeSafe's own docs use backticks
		// this way, so these must stay silent.
		"Is this `urgent` in the everyday sense?",
		"Treat `yes` and `no` as literal words.",
		"Does `extracted_value` match the `field` in `source_text`?",
		// Not path-shaped at all.
		"Does it match `{\"a\": 1}`?",
		"Consider `a b c`.",
		"Is the tone `polite, but firm`?",
	}
	for _, instr := range quiet {
		t.Run(instr, func(t *testing.T) {
			if w := refReq(instr).CheckReferences(); len(w) != 0 {
				t.Errorf("prose backticks warned: %v", w)
			}
		})
	}
}

// TestReferencesAgainstNonObjectState: a string state has no paths to resolve,
// so every reference into it is unresolvable — but that is the caller's
// choice, and a plain-string state is the documented simple case. Warn rather
// than error, and do not crash.
func TestReferencesAgainstStringState(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State:     "just a sentence",
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{Instructions: "Check `a.b`."}},
	}
	w := req.CheckReferences()
	if len(w) != 1 {
		t.Fatalf("got %d warnings, want 1", len(w))
	}
	if !strings.Contains(w[0].Reason, "not an object") {
		t.Errorf("Reason = %q", w[0].Reason)
	}
}

// TestReferencesUseTheMarshalledState: paths must be checked against the JSON
// actually sent, so a struct's json tags are what count — not its Go field
// names.
func TestReferencesUseTheMarshalledState(t *testing.T) {
	type ticket struct {
		Subject  string `json:"subject"`
		Internal string `json:"-"`
	}
	req := &typesafe.SystemOneRequest{
		State: struct {
			Ticket ticket `json:"ticket"`
		}{ticket{Subject: "hi", Internal: "secret"}},
		Questions: map[string]typesafe.Question{
			"tagged":   typesafe.Noul{Instructions: "Check `ticket.subject`."},
			"gofield":  typesafe.Noul{Instructions: "Check `ticket.Subject`."},
			"excluded": typesafe.Noul{Instructions: "Check `ticket.Internal`."},
		},
	}
	w := req.CheckReferences()
	if len(w) != 2 {
		t.Fatalf("got %d warnings, want 2 (the Go field name and the excluded field): %v", len(w), w)
	}
	for _, got := range w {
		if got.QuestionID == "tagged" {
			t.Error("the json-tagged path should resolve")
		}
	}
}

// TestSingleSegmentTyposAreNotCaught documents the deliberate blind spot, so
// that nobody later "fixes" it into a noisy checker without reading why.
//
// A bare `refund_polcy` is indistinguishable from emphasis or from a key of a
// structured instruction. Recall is traded for precision: the deep paths, where
// typos are both likelier and unambiguous, are still caught.
func TestSingleSegmentTyposAreNotCaught(t *testing.T) {
	if w := refReq("Does `refund_polcy` apply?").CheckReferences(); len(w) != 0 {
		t.Errorf("single-segment references should be left alone, got: %v", w)
	}
	// The same typo one level deeper is caught, because it is unambiguous.
	if w := refReq("Does `ticket.subjct` apply?").CheckReferences(); len(w) != 1 {
		t.Errorf("a multi-segment typo must still be caught, got %d warnings", len(w))
	}
}

func TestCheckReferencesHandlesEmptyInput(t *testing.T) {
	var nilReq *typesafe.SystemOneRequest
	if w := nilReq.CheckReferences(); w != nil {
		t.Errorf("nil request should produce no warnings, got %v", w)
	}
	empty := &typesafe.SystemOneRequest{}
	if w := empty.CheckReferences(); w != nil {
		t.Errorf("empty request should produce no warnings, got %v", w)
	}
}
