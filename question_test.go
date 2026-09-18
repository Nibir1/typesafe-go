package typesafe_test

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
)

// --- marshalling -------------------------------------------------------------

// TestQuestionsMarshalToTheWireForm checks each primitive against the exact
// JSON the API documents, including the details that are easy to get wrong:
// the type discriminator, an omitted optional, and a null option description.
func TestQuestionsMarshalToTheWireForm(t *testing.T) {
	cases := []struct {
		name string
		q    typesafe.Question
		want string
	}{
		{
			name: "noul minimal",
			q:    typesafe.Noul{Instructions: "Does this convey urgency?"},
			want: `{"type":"noul","instructions":"Does this convey urgency?"}`,
		},
		{
			name: "noul with criteria",
			q: typesafe.Noul{
				Instructions: "Does this convey urgency?",
				Criteria:     &typesafe.NoulCriteria{True: "Explicitly time-sensitive", False: "No urgency expressed"},
			},
			want: `{"type":"noul","instructions":"Does this convey urgency?","criteria":{"true":"Explicitly time-sensitive","false":"No urgency expressed"}}`,
		},
		{
			name: "noul with no instructions omits the field",
			q:    typesafe.Noul{Criteria: &typesafe.NoulCriteria{True: "yes"}},
			want: `{"type":"noul","criteria":{"true":"yes"}}`,
		},
		{
			name: "choice",
			q: typesafe.Choice{
				Instructions: "Which team should handle this?",
				Criteria:     typesafe.Options{"billing": "Payments, invoicing, refunds"},
			},
			want: `{"type":"choice","instructions":"Which team should handle this?","criteria":{"billing":"Payments, invoicing, refunds"}}`,
		},
		{
			// The canary: a nil description must be JSON null. Not "", not
			// an omitted key — the API distinguishes them.
			name: "choice with a null option description",
			q:    typesafe.Choice{Criteria: typesafe.Options{"Beaver": nil}},
			want: `{"type":"choice","criteria":{"Beaver":null}}`,
		},
		{
			name: "score",
			q: typesafe.Score{
				Instructions: "How frustrated is the customer?",
				Criteria:     typesafe.Levels{"Calm", "Frustrated", "Very angry"},
			},
			want: `{"type":"score","instructions":"How frustrated is the customer?","criteria":["Calm","Frustrated","Very angry"]}`,
		},
		{
			name: "structured instructions",
			q: typesafe.Noul{Instructions: map[string]any{
				"field":    map[string]any{"name": "invoice_number"},
				"question": "Does it match?",
			}},
			want: `{"type":"noul","instructions":{"field":{"name":"invoice_number"},"question":"Does it match?"}}`,
		},
		{
			name: "raw question passes through untouched",
			q:    typesafe.RawQuestion{"type": "noul", "instructions": "x"},
			want: `{"instructions":"x","type":"noul"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.q)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestScoreCriteriaOrderSurvives: a Score's meaning is positional, so any
// reordering silently changes every score the caller receives.
func TestScoreCriteriaOrderSurvives(t *testing.T) {
	levels := typesafe.Levels{"zero", "one", "two", "three", "four", "five"}
	b, err := json.Marshal(typesafe.Score{Criteria: levels})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Criteria []string `json:"criteria"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i, want := range []string{"zero", "one", "two", "three", "four", "five"} {
		if got.Criteria[i] != want {
			t.Errorf("level %d = %q, want %q", i, got.Criteria[i], want)
		}
	}
}

// TestTypedQuestionsReproduceGoldenFixtures is the strongest marshalling
// check available: rebuild each golden contract request using the typed
// primitives and assert it is semantically identical to the file that was
// replayed successfully against the live API.
func TestTypedQuestionsReproduceGoldenFixtures(t *testing.T) {
	cases := []struct {
		fixture   string
		questions map[string]typesafe.Question
	}{
		{
			fixture: "01_noul_single",
			questions: map[string]typesafe.Question{
				"is_urgent": typesafe.Noul{
					Instructions: "Does this convey urgency?",
					Criteria: &typesafe.NoulCriteria{
						True:  "Explicitly time-sensitive",
						False: "No urgency expressed",
					},
				},
			},
		},
		{
			fixture: "02_choice_single",
			questions: map[string]typesafe.Question{
				"department": typesafe.Choice{
					Instructions: "Which team should handle this?",
					Criteria: typesafe.Options{
						"billing":   "Payments, invoicing, refunds",
						"technical": "Bugs, outages, integrations",
						"sales":     "Pricing, upgrades, new accounts",
					},
				},
			},
		},
		{
			fixture: "03_score_single",
			questions: map[string]typesafe.Question{
				"frustration": typesafe.Score{
					Instructions: "How frustrated is the customer?",
					Criteria:     typesafe.Levels{"Calm", "Frustrated", "Very angry"},
				},
			},
		},
		{
			fixture: "07_null_criteria",
			questions: map[string]typesafe.Question{
				"customer_name": typesafe.Choice{
					Instructions: "Which option is the customer name in `source_text`?",
					Criteria: typesafe.Options{
						"Beaver Logistics": nil, "Dam Logistics": nil,
						"Beaver Dam Logistics": nil, "Beaver": nil, "Dam": nil,
					},
				},
				"bare_noul": typesafe.Noul{
					Instructions: "The invoice names a company.",
					Criteria:     &typesafe.NoulCriteria{True: "A company is named"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "contract", tc.fixture+".request.json"))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var want map[string]any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatalf("parse fixture: %v", err)
			}

			got, err := json.Marshal(tc.questions)
			if err != nil {
				t.Fatalf("marshal questions: %v", err)
			}
			var gotQ map[string]any
			if err := json.Unmarshal(got, &gotQ); err != nil {
				t.Fatalf("reparse: %v", err)
			}

			wantQ, _ := want["questions"].(map[string]any)
			if !jsonEqual(gotQ, wantQ) {
				g, _ := json.MarshalIndent(gotQ, "", "  ")
				w, _ := json.MarshalIndent(wantQ, "", "  ")
				t.Errorf("typed questions do not reproduce the fixture\n got: %s\nwant: %s", g, w)
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var x, y any
	_ = json.Unmarshal(ab, &x)
	_ = json.Unmarshal(bb, &y)
	return deepEqualJSON(x, y)
}

func deepEqualJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			w, ok := bv[k]
			if !ok || !deepEqualJSON(v, w) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqualJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// --- validation --------------------------------------------------------------

func TestValidateHardErrors(t *testing.T) {
	cases := map[string]*typesafe.SystemOneRequest{
		"no state":     {Questions: map[string]typesafe.Question{"q": typesafe.Noul{}}},
		"no questions": {State: "x"},
		"empty question id": {State: "x", Questions: map[string]typesafe.Question{
			"": typesafe.Noul{Instructions: "x"}}},
		"choice with no options": {State: "x", Questions: map[string]typesafe.Question{
			"q": typesafe.Choice{Instructions: "x"}}},
		"choice with an empty option name": {State: "x", Questions: map[string]typesafe.Question{
			"q": typesafe.Choice{Criteria: typesafe.Options{"": "x"}}}},
		"score with no levels": {State: "x", Questions: map[string]typesafe.Question{
			"q": typesafe.Score{Instructions: "x"}}},
		"raw question with no type": {State: "x", Questions: map[string]typesafe.Question{
			"q": typesafe.RawQuestion{"instructions": "x"}}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := req.Validate(); err == nil {
				t.Fatal("expected an error")
			} else if !errors.Is(err, typesafe.ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// TestScoreLevelCeilingIsEnforcedClientSide: the server rejects an 11-level
// Score with a 400, and that boundary appears in no published source. Catching
// it locally turns a wasted round trip into an immediate, explanatory error.
func TestScoreLevelCeilingIsEnforcedClientSide(t *testing.T) {
	levels := make(typesafe.Levels, typesafe.MaxScoreLevels+1)
	for i := range levels {
		levels[i] = "level"
	}
	req := &typesafe.SystemOneRequest{
		State:     "x",
		Questions: map[string]typesafe.Question{"sev": typesafe.Score{Criteria: levels}},
	}
	_, err := req.Validate()
	if err == nil {
		t.Fatal("expected an error for 11 levels")
	}
	if !strings.Contains(err.Error(), "at most 10") {
		t.Errorf("the error should state the limit, got: %v", err)
	}

	// Exactly at the ceiling must be accepted.
	req.Questions["sev"] = typesafe.Score{Criteria: levels[:typesafe.MaxScoreLevels]}
	if _, err := req.Validate(); err != nil {
		t.Errorf("%d levels should be legal, got: %v", typesafe.MaxScoreLevels, err)
	}
}

// TestSingleLevelScoreIsLegal: the prose docs claim a two-level minimum. The
// live API accepts one. This SDK does not reject what the server accepts.
func TestSingleLevelScoreIsLegal(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State: "x",
		Questions: map[string]typesafe.Question{
			"q": typesafe.Score{Instructions: "Rate it.", Criteria: typesafe.Levels{"only level"}},
		},
	}
	warnings, err := req.Validate()
	if err != nil {
		t.Fatalf("a one-level Score must be legal: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("a one-level Score should warn even though it is legal")
	}
}

func TestValidateWarnings(t *testing.T) {
	cases := map[string]struct {
		q    typesafe.Question
		want string
	}{
		"empty noul":                  {typesafe.Noul{}, "says nothing about what to judge"},
		"one-option choice":           {typesafe.Choice{Instructions: "x", Criteria: typesafe.Options{"only": nil}}, "nothing to choose between"},
		"choice with no instructions": {typesafe.Choice{Criteria: typesafe.Options{"a": "x", "b": "y"}}, "option names alone"},
		"score with a nil level":      {typesafe.Score{Instructions: "x", Criteria: typesafe.Levels{"a", nil}}, "no description"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := &typesafe.SystemOneRequest{State: "x",
				Questions: map[string]typesafe.Question{"q": tc.q}}
			warnings, err := req.Validate()
			if err != nil {
				t.Fatalf("should warn, not error: %v", err)
			}
			var found bool
			for _, w := range warnings {
				if strings.Contains(w.Message, tc.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("no warning containing %q; got %v", tc.want, warnings)
			}
		})
	}
}

// TestWarningsAreDeterministic: warning order must not depend on Go's random
// map iteration, or logs and snapshot tests become unstable.
func TestWarningsAreDeterministic(t *testing.T) {
	req := &typesafe.SystemOneRequest{State: "x", Questions: map[string]typesafe.Question{
		"a": typesafe.Noul{}, "b": typesafe.Noul{}, "c": typesafe.Noul{},
		"d": typesafe.Noul{}, "e": typesafe.Noul{},
	}}
	first, err := req.Validate()
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	for i := range 50 {
		got, err := req.Validate()
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if len(got) != len(first) {
			t.Fatalf("run %d: %d warnings, want %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d: warning %d = %v, want %v", i, j, got[j], first[j])
			}
		}
	}
}

// --- answers -----------------------------------------------------------------

func decodeResponse(t *testing.T, body string) *typesafe.SystemOneResponse {
	t.Helper()
	var r typesafe.SystemOneResponse
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &r
}

const mixedBody = `{
  "model": "jev-1.13.0",
  "answers": {
    "is_urgent": {"type":"noul","noul":0.92},
    "department": {"type":"choice","choice":"technical",
      "probabilities":{"billing":0.08,"technical":0.85,"sales":0.07},"confidence":0.82},
    "frustration": {"type":"score","score":1.6,
      "legend":{"0":"Calm","1":"Frustrated","2":"Very angry"},
      "probabilities":{"0":0.05,"1":0.3,"2":0.65},"confidence":0.78}
  },
  "usage": {"input_tokens": 312, "output_tokens": 48}
}`

func TestTypedAccessors(t *testing.T) {
	r := decodeResponse(t, mixedBody)

	n, err := r.Noul("is_urgent")
	if err != nil || n.Noul != 0.92 {
		t.Errorf("Noul = %v, %v", n, err)
	}
	c, err := r.Choice("department")
	if err != nil || c.Choice != "technical" || c.Confidence != 0.82 {
		t.Errorf("Choice = %+v, %v", c, err)
	}
	s, err := r.Score("frustration")
	if err != nil || s.Score != 1.6 || s.NumLevels() != 3 {
		t.Errorf("Score = %+v, %v", s, err)
	}
}

func TestWrongAccessorReturnsAnError(t *testing.T) {
	r := decodeResponse(t, mixedBody)

	if _, err := r.Choice("is_urgent"); !errors.Is(err, typesafe.ErrWrongAnswerType) {
		t.Errorf("Choice on a noul: err = %v, want ErrWrongAnswerType", err)
	}
	if _, err := r.Noul("frustration"); !errors.Is(err, typesafe.ErrWrongAnswerType) {
		t.Errorf("Noul on a score: err = %v, want ErrWrongAnswerType", err)
	}
	if _, err := r.Score("nope"); !errors.Is(err, typesafe.ErrNoSuchAnswer) {
		t.Errorf("missing id: err = %v, want ErrNoSuchAnswer", err)
	}
	// The message should say what it actually is, not just that it is wrong.
	_, err := r.Choice("is_urgent")
	if !strings.Contains(err.Error(), "noul") {
		t.Errorf("error should name the actual type, got: %v", err)
	}
}

func TestDiscriminatedAnswer(t *testing.T) {
	r := decodeResponse(t, mixedBody)
	for id, wantType := range map[string]string{
		"is_urgent": "noul", "department": "choice", "frustration": "score",
	} {
		a, err := r.Answer(id)
		if err != nil {
			t.Fatalf("Answer(%q): %v", id, err)
		}
		if a.Type() != wantType {
			t.Errorf("Answer(%q).Type() = %q, want %q", id, a.Type(), wantType)
		}
	}
	if got := len(r.All()); got != 3 {
		t.Errorf("All() has %d entries, want 3", got)
	}
	if got := len(r.Nouls()); got != 1 {
		t.Errorf("Nouls() has %d entries, want 1", got)
	}
	if got := len(r.Choices()); got != 1 {
		t.Errorf("Choices() has %d entries, want 1", got)
	}
	if got := len(r.Scores()); got != 1 {
		t.Errorf("Scores() has %d entries, want 1", got)
	}
}

// TestConfidenceReportsAbsenceForNoul guards the trap that motivated modelling
// NoulAnswer without a Confidence field. Returning 0 would read as "maximally
// uncertain" for every Noul, which is the opposite of the truth for a 0.92.
func TestConfidenceReportsAbsenceForNoul(t *testing.T) {
	r := decodeResponse(t, mixedBody)

	if _, ok := r.Confidence("is_urgent"); ok {
		t.Error("a noul must report that it has no confidence, not a zero")
	}
	if c, ok := r.Confidence("department"); !ok || c != 0.82 {
		t.Errorf("Confidence(choice) = %v, %v", c, ok)
	}
	if c, ok := r.Confidence("frustration"); !ok || c != 0.78 {
		t.Errorf("Confidence(score) = %v, %v", c, ok)
	}
}

func TestNoulHelpers(t *testing.T) {
	a := typesafe.NoulAnswer{Noul: 0.92}
	if !a.Bool(0.5) || a.Bool(0.95) {
		t.Error("Bool thresholding is wrong")
	}
	if a.Uncertain(0.1) {
		t.Error("0.92 is not uncertain")
	}
	if !(typesafe.NoulAnswer{Noul: 0.52}).Uncertain(0.1) {
		t.Error("0.52 should be uncertain at delta 0.1")
	}
}

// TestNoulNilCriteriaMemberIsOmittedNotNulled pins a choice that is easy to
// make silently either way. For a Noul the two encodings are equivalent, so
// the SDK sends the shorter one; for a Choice they are not, and the null must
// survive. Both halves are asserted here so neither drifts.
func TestNoulNilCriteriaMemberIsOmittedNotNulled(t *testing.T) {
	b, err := json.Marshal(typesafe.Noul{Criteria: &typesafe.NoulCriteria{True: "yes"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "false") {
		t.Errorf("a nil Noul criteria member should be omitted, got: %s", b)
	}

	b, err = json.Marshal(typesafe.Choice{Criteria: typesafe.Options{"only": nil}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"only":null`) {
		t.Errorf("a nil Choice option description must be an explicit null, got: %s", b)
	}
}

func TestChoiceHelpers(t *testing.T) {
	a := typesafe.ChoiceAnswer{
		Choice:        "technical",
		Probabilities: map[string]float64{"billing": 0.08, "technical": 0.85, "sales": 0.07},
		Confidence:    0.82,
	}
	ranked := a.Ranked()
	if ranked[0].Option != "technical" || ranked[1].Option != "billing" || ranked[2].Option != "sales" {
		t.Errorf("Ranked = %+v", ranked)
	}
	if got := a.Margin(); math.Abs(got-0.77) > 1e-9 {
		t.Errorf("Margin = %v, want 0.77", got)
	}
	if p, ok := a.ProbabilityOf("billing"); !ok || p != 0.08 {
		t.Errorf("ProbabilityOf(billing) = %v, %v", p, ok)
	}
	if _, ok := a.ProbabilityOf("nope"); ok {
		t.Error("ProbabilityOf should report an unknown option as absent")
	}

	// Ties must break deterministically, or rankings vary run to run.
	tied := typesafe.ChoiceAnswer{Probabilities: map[string]float64{"b": 0.5, "a": 0.5}}
	for range 50 {
		if r := tied.Ranked(); r[0].Option != "a" {
			t.Fatalf("tie-break is not deterministic: got %v first", r[0].Option)
		}
	}
}

func TestScoreHelpers(t *testing.T) {
	a := typesafe.ScoreAnswer{
		Score:         1.6,
		Legend:        map[string]any{"0": "Calm", "1": "Frustrated", "2": "Very angry"},
		Probabilities: map[string]float64{"0": 0.05, "1": 0.3, "2": 0.65},
		Confidence:    0.78,
	}
	if got := a.Levels(); got[0] != "Calm" || got[1] != "Frustrated" || got[2] != "Very angry" {
		t.Errorf("Levels = %v, want index order", got)
	}
	if got := a.LevelProbabilities(); got[0] != 0.05 || got[2] != 0.65 {
		t.Errorf("LevelProbabilities = %v", got)
	}
	if i, label := a.Nearest(); i != 2 || label != "Very angry" {
		t.Errorf("Nearest = %d, %v; want 2, \"Very angry\"", i, label)
	}
	if i, p := a.MostLikely(); i != 2 || p != 0.65 {
		t.Errorf("MostLikely = %d, %v", i, p)
	}
	if got := a.AtOrAbove(1); math.Abs(got-0.95) > 1e-9 {
		t.Errorf("AtOrAbove(1) = %v, want 0.95", got)
	}
	if got := a.AtOrBelow(1); math.Abs(got-0.35) > 1e-9 {
		t.Errorf("AtOrBelow(1) = %v, want 0.35", got)
	}
}

// TestScoreWithStructuredLegend: the prose docs type legend as
// map<string,string>, but the schema and the live API allow structured level
// descriptions. Modelling it as string silently breaks structured rubrics.
func TestScoreWithStructuredLegend(t *testing.T) {
	r := decodeResponse(t, `{
	  "model":"jev-1.13.0",
	  "answers":{"severity":{"type":"score","score":1.83,
	    "legend":{"0":{"level":"none"},"1":{"level":"partial"},"2":{"level":"blocked"}},
	    "probabilities":{"0":0.04,"1":0.09,"2":0.87},"confidence":0.84}},
	  "usage":{"input_tokens":1,"output_tokens":1}}`)

	s, err := r.Score("severity")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	levels := s.Levels()
	m, ok := levels[2].(map[string]any)
	if !ok {
		t.Fatalf("level 2 is %T, want a structured object", levels[2])
	}
	if m["level"] != "blocked" {
		t.Errorf("level 2 = %v", m)
	}
}

// TestMostLikelyCanDisagreeWithNearest documents a real reading hazard: on a
// bimodal distribution the weighted mean lands in a level the model considers
// unlikely. Anyone thresholding on Score alone would act on a middle value
// that nothing supports.
func TestMostLikelyCanDisagreeWithNearest(t *testing.T) {
	a := typesafe.ScoreAnswer{
		Score:         1.0, // 0*0.5 + 1*0.0 + 2*0.5
		Legend:        map[string]any{"0": "low", "1": "middle", "2": "high"},
		Probabilities: map[string]float64{"0": 0.5, "1": 0.0, "2": 0.5},
	}
	near, _ := a.Nearest()
	likely, p := a.MostLikely()
	if near != 1 {
		t.Errorf("Nearest = %d, want 1", near)
	}
	if likely == 1 || p != 0.5 {
		t.Errorf("MostLikely = %d (%v); the middle level has probability 0", likely, p)
	}
	if got := a.Probabilities["1"]; got != 0 {
		t.Fatalf("fixture is wrong: middle probability is %v", got)
	}
}

// --- edge cases --------------------------------------------------------------

// TestAnswerHelpersOnDegenerateInput covers the branches a well-formed
// response never reaches: an empty distribution, a single option, a score
// outside its legend. These are reachable from a hand-built answer, and from
// any future server change, so none of them may panic.
func TestAnswerHelpersOnDegenerateInput(t *testing.T) {
	t.Run("empty choice", func(t *testing.T) {
		var a typesafe.ChoiceAnswer
		if got := a.Margin(); got != 0 {
			t.Errorf("Margin on an empty answer = %v, want 0", got)
		}
		if got := a.Ranked(); len(got) != 0 {
			t.Errorf("Ranked = %v, want empty", got)
		}
	})

	t.Run("single-option choice", func(t *testing.T) {
		a := typesafe.ChoiceAnswer{Choice: "only", Probabilities: map[string]float64{"only": 1}}
		if got := a.Margin(); got != 1 {
			t.Errorf("Margin with one option = %v, want its probability (1)", got)
		}
	})

	t.Run("empty score", func(t *testing.T) {
		var a typesafe.ScoreAnswer
		if i, label := a.Nearest(); i != -1 || label != nil {
			t.Errorf("Nearest on an empty legend = %d, %v; want -1, nil", i, label)
		}
		if i, p := a.MostLikely(); i != -1 || p != 0 {
			t.Errorf("MostLikely on an empty answer = %d, %v; want -1, 0", i, p)
		}
		if got := a.NumLevels(); got != 0 {
			t.Errorf("NumLevels = %d", got)
		}
	})

	t.Run("score clamps outside its legend", func(t *testing.T) {
		legend := map[string]any{"0": "low", "1": "high"}
		hi := typesafe.ScoreAnswer{Score: 99, Legend: legend}
		if i, label := hi.Nearest(); i != 1 || label != "high" {
			t.Errorf("Nearest above the top = %d, %v; want it clamped to 1", i, label)
		}
		lo := typesafe.ScoreAnswer{Score: -5, Legend: legend}
		if i, label := lo.Nearest(); i != 0 || label != "low" {
			t.Errorf("Nearest below zero = %d, %v; want it clamped to 0", i, label)
		}
	})

	t.Run("non-numeric legend keys sort last without panicking", func(t *testing.T) {
		a := typesafe.ScoreAnswer{
			Legend:        map[string]any{"1": "one", "weird": "?", "0": "zero"},
			Probabilities: map[string]float64{"1": 0.5, "weird": 0.2, "0": 0.3},
		}
		levels := a.Levels()
		if len(levels) != 3 || levels[0] != "zero" || levels[1] != "one" || levels[2] != "?" {
			t.Errorf("Levels = %v; numeric keys should come first, in order", levels)
		}
		if got := a.AtOrAbove(0); got != 0.8 {
			t.Errorf("AtOrAbove(0) = %v; unparseable keys should be skipped, not counted", got)
		}
	})
}

// TestMarshalWithNilCriteria: criteria is a required field, so it must be
// emitted even when the caller left it nil. Validate rejects that request, but
// marshalling must still produce legal JSON rather than an omitted key.
func TestMarshalWithNilCriteria(t *testing.T) {
	b, err := json.Marshal(typesafe.Choice{Instructions: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"criteria":{}`) {
		t.Errorf("Choice with nil criteria = %s, want an empty object", b)
	}

	b, err = json.Marshal(typesafe.Score{Instructions: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"criteria":[]`) {
		t.Errorf("Score with nil criteria = %s, want an empty array", b)
	}
}

func TestWarningString(t *testing.T) {
	w := typesafe.Warning{QuestionID: "q", Message: "something odd"}
	if got := w.String(); got != `question "q": something odd` {
		t.Errorf("String() = %q", got)
	}
	bare := typesafe.Warning{Message: "request-level"}
	if got := bare.String(); got != "request-level" {
		t.Errorf("String() with no question id = %q", got)
	}
}
