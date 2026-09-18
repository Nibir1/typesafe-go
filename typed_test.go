package typesafe_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
)

// The option set from testdata/contract/02_choice_single.
type Topic string

const (
	TopicBilling   Topic = "billing"
	TopicTechnical Topic = "technical"
	TopicSales     Topic = "sales"
)

var AllTopics = []Topic{TopicBilling, TopicTechnical, TopicSales}

// The rubric from testdata/contract/09_score_max_levels — ten levels, the
// documented ceiling.
type Severity int

const (
	SevNone Severity = iota
	SevTrivial
	SevMinor
	SevNoticeable
	SevModerate
	SevElevated
	SevSerious
	SevSevere
	SevCritical
	SevCatastrophic
)

func severityRubric() typesafe.TypedScoreQuestion[Severity] {
	return typesafe.TypedScore[Severity]("Rate incident severity.",
		typesafe.LevelOf(SevNone, "Level 0: no impact"),
		typesafe.LevelOf(SevTrivial, "Level 1: trivial"),
		typesafe.LevelOf(SevMinor, "Level 2: minor"),
		typesafe.LevelOf(SevNoticeable, "Level 3: noticeable"),
		typesafe.LevelOf(SevModerate, "Level 4: moderate"),
		typesafe.LevelOf(SevElevated, "Level 5: elevated"),
		typesafe.LevelOf(SevSerious, "Level 6: serious"),
		typesafe.LevelOf(SevSevere, "Level 7: severe"),
		typesafe.LevelOf(SevCritical, "Level 8: critical"),
		typesafe.LevelOf(SevCatastrophic, "Level 9: catastrophic"),
	)
}

// loadFixtureQuestion returns one question object from a contract fixture, so
// the typed constructors are compared against the locked wire contract rather
// than against another copy of this package's own opinion.
func loadFixtureQuestion(t *testing.T, fixture, id string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "contract", fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var req struct {
		Questions map[string]any `json:"questions"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	q, ok := req.Questions[id]
	if !ok {
		t.Fatalf("fixture %s has no question %q", fixture, id)
	}
	return q
}

func loadFixtureResponse(t *testing.T, fixture string) *typesafe.SystemOneResponse {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "contract", fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var resp typesafe.SystemOneResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return &resp
}

// asJSON round-trips through JSON so two values are compared as the wire sees
// them, not as Go types.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// --- wire equality -----------------------------------------------------------

// The headline exit criterion. The typed wrapper embeds the plain question and
// marshals through the same code, so this asserts a property the design makes
// true rather than one it has to maintain — which is the point of embedding
// rather than writing a parallel marshaller.
func TestTypedChoiceMarshalsIdentically(t *testing.T) {
	plain := typesafe.Choice{
		Instructions: "Which team should handle this?",
		Criteria: typesafe.Options{
			"billing":   "Payments, invoicing, refunds",
			"technical": "Bugs, outages, integrations",
			"sales":     "Pricing, upgrades, new accounts",
		},
	}
	typed := typesafe.TypedChoice[Topic]("Which team should handle this?",
		typesafe.OptionOf(TopicBilling, "Payments, invoicing, refunds"),
		typesafe.OptionOf(TopicTechnical, "Bugs, outages, integrations"),
		typesafe.OptionOf(TopicSales, "Pricing, upgrades, new accounts"),
	)

	plainJSON, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	typedJSON, err := json.Marshal(typed)
	if err != nil {
		t.Fatalf("marshal typed: %v", err)
	}
	if string(plainJSON) != string(typedJSON) {
		t.Errorf("typed and plain differ:\n plain: %s\n typed: %s", plainJSON, typedJSON)
	}

	// And both match the locked contract fixture.
	want := loadFixtureQuestion(t, "02_choice_single.request.json", "department")
	if got := asJSON(t, typed); !reflect.DeepEqual(got, want) {
		t.Errorf("typed question does not match the fixture:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestTypedScoreMarshalsIdentically(t *testing.T) {
	plain := typesafe.Score{
		Instructions: "Rate incident severity.",
		Criteria: typesafe.Levels{
			"Level 0: no impact", "Level 1: trivial", "Level 2: minor",
			"Level 3: noticeable", "Level 4: moderate", "Level 5: elevated",
			"Level 6: serious", "Level 7: severe", "Level 8: critical",
			"Level 9: catastrophic",
		},
	}
	typed := severityRubric()

	plainJSON, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	typedJSON, err := json.Marshal(typed)
	if err != nil {
		t.Fatalf("marshal typed: %v", err)
	}
	if string(plainJSON) != string(typedJSON) {
		t.Errorf("typed and plain differ:\n plain: %s\n typed: %s", plainJSON, typedJSON)
	}

	want := loadFixtureQuestion(t, "09_score_max_levels.request.json", "severity")
	if got := asJSON(t, typed); !reflect.DeepEqual(got, want) {
		t.Errorf("typed question does not match the fixture:\n got: %#v\nwant: %#v", got, want)
	}
}

// A nil description must stay JSON null, not become an empty string — the
// contract distinguishes them, and null means "read this option by its name".
func TestTypedChoiceNilDescriptionStaysNull(t *testing.T) {
	typed := typesafe.TypedChoice[Topic]("Which team?",
		typesafe.OptionOf(TopicBilling, "Payments"),
		typesafe.OptionOf(TopicOtherForNull, nil),
	)
	b, err := json.Marshal(typed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Criteria map[string]any `json:"criteria"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := got.Criteria["other"]
	if !ok {
		t.Fatal(`criteria has no "other" key`)
	}
	if v != nil {
		t.Errorf(`criteria["other"] = %#v, want null`, v)
	}
}

const TopicOtherForNull Topic = "other"

// A typed question must go through the same request path as a plain one.
func TestTypedQuestionInARequest(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: map[string]typesafe.Question{
			"department": typesafe.TypedChoice[Topic]("Which team should handle this?",
				typesafe.OptionOf(TopicBilling, "Payments, invoicing, refunds"),
				typesafe.OptionOf(TopicTechnical, "Bugs, outages, integrations"),
				typesafe.OptionOf(TopicSales, "Pricing, upgrades, new accounts"),
			),
			"severity": severityRubric(),
		},
	}
	warnings, err := req.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

// --- validation of what only the wrapper knows -------------------------------

// A duplicate option silently collapses in the criteria map, so the request
// would carry fewer options than the code appears to declare.
func TestTypedChoiceRejectsDuplicateOptions(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State: "x",
		Questions: map[string]typesafe.Question{
			"dept": typesafe.TypedChoice[Topic]("Which team?",
				typesafe.OptionOf(TopicBilling, "Payments"),
				typesafe.OptionOf(TopicBilling, "Payments again"),
				typesafe.OptionOf(TopicSales, "Pricing"),
			),
		},
	}
	_, err := req.Validate()
	if !errors.Is(err, typesafe.ErrInvalidRequest) {
		t.Fatalf("Validate err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("error should say what went wrong, got: %v", err)
	}
}

// A rubric declared out of order maps every answer to the wrong label, and
// nothing in the resulting score reveals it.
func TestTypedScoreRejectsMisorderedLevels(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State: "x",
		Questions: map[string]typesafe.Question{
			"sev": typesafe.TypedScore[Severity]("Rate it.",
				typesafe.LevelOf(SevCritical, "critical"), // position 0, value 8
				typesafe.LevelOf(SevNone, "none"),
			),
		},
	}
	_, err := req.Validate()
	if !errors.Is(err, typesafe.ErrInvalidRequest) {
		t.Fatalf("Validate err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "position is its score") {
		t.Errorf("error should explain the ordering rule, got: %v", err)
	}
}

// The plain question's own rules still apply underneath the wrapper: this is
// the trap the fluent builders fell into, where a wrapper marshalled fine and
// then failed validation as an "unknown type".
func TestTypedScoreStillHitsTheLevelCeiling(t *testing.T) {
	levels := make([]typesafe.TypedLevel[Severity], 0, 11)
	for i := 0; i < 11; i++ {
		levels = append(levels, typesafe.LevelOf(Severity(i), "level"))
	}
	req := &typesafe.SystemOneRequest{
		State:     "x",
		Questions: map[string]typesafe.Question{"sev": typesafe.TypedScore("Rate it.", levels...)},
	}
	_, err := req.Validate()
	if !errors.Is(err, typesafe.ErrInvalidRequest) {
		t.Fatalf("Validate err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "at most 10 levels") {
		t.Errorf("error should name the ceiling, got: %v", err)
	}
}

func TestTypedChoiceEmptyIsRejected(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State:     "x",
		Questions: map[string]typesafe.Question{"dept": typesafe.TypedChoice[Topic]("Which team?")},
	}
	if _, err := req.Validate(); !errors.Is(err, typesafe.ErrInvalidRequest) {
		t.Fatalf("Validate err = %v, want ErrInvalidRequest", err)
	}
}

// --- typed answers -----------------------------------------------------------

func TestTypedChoiceAnswer(t *testing.T) {
	resp := loadFixtureResponse(t, "02_choice_single.response.json")

	ans, err := typesafe.TypedChoiceAnswer[Topic](resp, "department")
	if err != nil {
		t.Fatalf("TypedChoiceAnswer: %v", err)
	}

	plain, err := resp.Choice("department")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}

	if string(ans.Choice) != plain.Choice {
		t.Errorf("typed choice %q != plain %q", ans.Choice, plain.Choice)
	}
	if ans.Confidence != plain.Confidence {
		t.Errorf("confidence %v != %v", ans.Confidence, plain.Confidence)
	}
	if len(ans.Probabilities) != len(plain.Probabilities) {
		t.Fatalf("%d typed probabilities, %d plain", len(ans.Probabilities), len(plain.Probabilities))
	}
	for k, p := range plain.Probabilities {
		if ans.Probabilities[Topic(k)] != p {
			t.Errorf("probability for %q: typed %v, plain %v", k, ans.Probabilities[Topic(k)], p)
		}
	}

	// The typed form must agree with the plain one on every derived value.
	if ans.Margin() != plain.Margin() {
		t.Errorf("Margin: typed %v, plain %v", ans.Margin(), plain.Margin())
	}
	typedRanked, plainRanked := ans.Ranked(), plain.Ranked()
	if len(typedRanked) != len(plainRanked) {
		t.Fatalf("Ranked lengths differ: %d vs %d", len(typedRanked), len(plainRanked))
	}
	for i := range typedRanked {
		if string(typedRanked[i].Option) != plainRanked[i].Option {
			t.Errorf("Ranked[%d]: typed %q, plain %q", i, typedRanked[i].Option, plainRanked[i].Option)
		}
	}
	if got := asJSON(t, ans.Untyped()); !reflect.DeepEqual(got, asJSON(t, plain)) {
		t.Errorf("Untyped() does not reproduce the plain answer")
	}

	// A switch on the typed value compiles against the enum, which is the
	// whole point: a typo here would not build.
	switch ans.Choice {
	case TopicBilling, TopicTechnical, TopicSales:
	default:
		t.Errorf("answer %q is outside the declared set", ans.Choice)
	}
}

// The declared rubric must round-trip through a full ten-level answer: every
// index maps back to the label the question declared for it.
func TestTypedScoreAnswerRoundTripsTenLevels(t *testing.T) {
	resp := loadFixtureResponse(t, "09_score_max_levels.response.json")
	q := severityRubric()

	ans, err := q.Answer(resp, "severity")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	plain, err := resp.Score("severity")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	if len(ans.Legend) != 10 || len(ans.Probabilities) != 10 {
		t.Fatalf("legend=%d probabilities=%d, want 10 and 10", len(ans.Legend), len(ans.Probabilities))
	}

	// Every declared level appears, and its legend entry is the description
	// the question was built with.
	declared := q.Levels()
	criteria := q.Score.Criteria
	for i, lvl := range declared {
		if int(lvl) != i {
			t.Fatalf("declared level %d has value %d", i, lvl)
		}
		got, ok := ans.Legend[lvl]
		if !ok {
			t.Fatalf("level %d missing from the legend", i)
		}
		if got != criteria[i] {
			t.Errorf("level %d legend = %v, want %v", i, got, criteria[i])
		}
	}

	if ans.Score != plain.Score {
		t.Errorf("score: typed %v, plain %v", ans.Score, plain.Score)
	}
	nearest, _ := plain.Nearest()
	if int(ans.Level) != nearest {
		t.Errorf("Level = %d, want the nearest level %d", ans.Level, nearest)
	}

	// Thresholding must agree with the untyped form at every level.
	for i, lvl := range declared {
		if got, want := ans.AtOrAbove(lvl), plain.AtOrAbove(i); !nearlyEqual(got, want) {
			t.Errorf("AtOrAbove(%d): typed %v, plain %v", i, got, want)
		}
		if got, want := ans.AtOrBelow(lvl), plain.AtOrBelow(i); !nearlyEqual(got, want) {
			t.Errorf("AtOrBelow(%d): typed %v, plain %v", i, got, want)
		}
	}

	typedLikely, typedP := ans.MostLikely()
	plainLikely, plainP := plain.MostLikely()
	if int(typedLikely) != plainLikely || typedP != plainP {
		t.Errorf("MostLikely: typed (%d, %v), plain (%d, %v)",
			typedLikely, typedP, plainLikely, plainP)
	}

	// This fixture is the case the docs warn about: the weighted mean sits at
	// level 7 while the single most likely level is 9.
	if int(ans.Level) == plainLikely {
		t.Log("note: this fixture no longer demonstrates Level != MostLikely")
	}

	if got := asJSON(t, ans.Untyped()); !reflect.DeepEqual(got, asJSON(t, plain)) {
		t.Errorf("Untyped() does not reproduce the plain answer")
	}
}

func nearlyEqual(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

// --- exhaustiveness ----------------------------------------------------------

func TestExhaustive(t *testing.T) {
	resp := loadFixtureResponse(t, "02_choice_single.response.json")
	ans, err := typesafe.TypedChoiceAnswer[Topic](resp, "department")
	if err != nil {
		t.Fatalf("TypedChoiceAnswer: %v", err)
	}

	if err := typesafe.Exhaustive(ans, AllTopics...); err != nil {
		t.Errorf("Exhaustive over the full set: %v", err)
	}
	if err := typesafe.Exhaustive(ans); err != nil {
		t.Errorf("Exhaustive with no declared set should check nothing, got %v", err)
	}

	// Drop one, and the answer's distribution is no longer covered.
	err = typesafe.Exhaustive(ans, TopicBilling, TopicTechnical)
	if !errors.Is(err, typesafe.ErrUndeclaredOption) {
		t.Fatalf("err = %v, want ErrUndeclaredOption", err)
	}
	if !strings.Contains(err.Error(), "sales") {
		t.Errorf("error should name the undeclared option, got: %v", err)
	}
}

// The question's own Answer method checks against the set it declared, which
// is the case a free-standing decode cannot cover.
func TestTypedQuestionAnswerDetectsDrift(t *testing.T) {
	resp := loadFixtureResponse(t, "02_choice_single.response.json")

	// A question that has drifted from the response: sales was never declared.
	q := typesafe.TypedChoice[Topic]("Which team should handle this?",
		typesafe.OptionOf(TopicBilling, "Payments, invoicing, refunds"),
		typesafe.OptionOf(TopicTechnical, "Bugs, outages, integrations"),
	)
	_, err := q.Answer(resp, "department")
	if !errors.Is(err, typesafe.ErrUndeclaredOption) {
		t.Fatalf("err = %v, want ErrUndeclaredOption", err)
	}

	// The matching question accepts it.
	full := typesafe.TypedChoice[Topic]("Which team should handle this?",
		typesafe.OptionOf(TopicBilling, "Payments, invoicing, refunds"),
		typesafe.OptionOf(TopicTechnical, "Bugs, outages, integrations"),
		typesafe.OptionOf(TopicSales, "Pricing, upgrades, new accounts"),
	)
	if _, err := full.Answer(resp, "department"); err != nil {
		t.Errorf("Answer with the full set: %v", err)
	}
}

// The error must be deterministic: it is built from a map, and a message that
// reorders between runs breaks log search and snapshot tests.
func TestExhaustiveErrorIsDeterministic(t *testing.T) {
	ans := typesafe.ChoiceAnswerOf[Topic]{
		Choice: "zeta",
		Probabilities: map[Topic]float64{
			"zeta": 0.4, "alpha": 0.3, "mu": 0.2, "beta": 0.1,
		},
	}
	first := typesafe.Exhaustive(ans, TopicBilling).Error()
	for i := 0; i < 50; i++ {
		if got := typesafe.Exhaustive(ans, TopicBilling).Error(); got != first {
			t.Fatalf("message varies between runs:\n %s\n %s", first, got)
		}
	}
	if !strings.Contains(first, "[alpha beta mu zeta]") {
		t.Errorf("undeclared options should be sorted, got: %s", first)
	}
}

// --- error propagation -------------------------------------------------------

func TestTypedAnswerErrorsPropagate(t *testing.T) {
	resp := loadFixtureResponse(t, "02_choice_single.response.json")

	if _, err := typesafe.TypedChoiceAnswer[Topic](resp, "nope"); !errors.Is(err, typesafe.ErrNoSuchAnswer) {
		t.Errorf("err = %v, want ErrNoSuchAnswer", err)
	}
	if _, err := typesafe.TypedScoreAnswer[Severity](resp, "department"); !errors.Is(err, typesafe.ErrWrongAnswerType) {
		t.Errorf("err = %v, want ErrWrongAnswerType", err)
	}
}

// A legend key that is not a number is an error, not a silently dropped level.
// Every other accessor in this SDK sorts unparseable keys last and keeps going;
// here the key *is* the typed value, so there is nothing to map it to.
func TestTypedScoreAnswerRejectsNonNumericKeys(t *testing.T) {
	resp := &typesafe.SystemOneResponse{
		Model: "jev-1.13.0",
		Answers: map[string]json.RawMessage{
			"sev": json.RawMessage(`{
				"type": "score",
				"score": 1.0,
				"legend": {"0": "low", "high": "oops"},
				"probabilities": {"0": 0.5, "1": 0.5},
				"confidence": 0.9
			}`),
		},
	}
	_, err := typesafe.TypedScoreAnswer[Severity](resp, "sev")
	if err == nil {
		t.Fatal("expected an error for a non-numeric legend key")
	}
	if !strings.Contains(err.Error(), "non-numeric") {
		t.Errorf("error should say what is wrong, got: %v", err)
	}
}

// --- the untyped escape hatch ------------------------------------------------

// Converting to the typed form and back must be lossless, so a caller can move
// to typed questions without giving up the decision package or anything else
// built on the plain answers.
func TestUntypedRoundTrip(t *testing.T) {
	resp := loadFixtureResponse(t, "02_choice_single.response.json")
	plain, err := resp.Choice("department")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	typed, err := typesafe.TypedChoiceAnswer[Topic](resp, "department")
	if err != nil {
		t.Fatalf("TypedChoiceAnswer: %v", err)
	}
	if !reflect.DeepEqual(typed.Untyped(), plain) {
		t.Errorf("round trip lost information:\n got: %#v\nwant: %#v", typed.Untyped(), plain)
	}
}
