package contract_test

// Phase 0 contract lock.
//
// These tests encode the published wire contract as executable assertions, so
// that Phases 1 and 2 have something to be correct against before a single
// type is written. They run entirely offline against testdata/contract.
//
// The integration-gated drift test lives in contract_integration_test.go and
// replays the same fixtures against the live API.

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/nibir1/typesafe-go/internal/fixtures"
)

// maxScoreLevels is the server-enforced ceiling on Score rubric levels.
// Undocumented in both the OpenAPI schema and the prose docs; established by
// sending eleven and receiving 400 "Too many score levels. Must have at most
// 10 levels."
const maxScoreLevels = 10

// tolerance for float comparisons on probability distributions.
const eps = 1e-6

type fixture = fixtures.Fixture

func loadFixtures(t *testing.T) []fixture { return fixtures.All(t) }

func readJSON(t *testing.T, path string) map[string]any { return fixtures.ReadJSON(t, path) }

func num(t *testing.T, v any, ctx string) float64 {
	t.Helper()
	n, ok := v.(json.Number)
	if !ok {
		t.Fatalf("%s: expected a number, got %T", ctx, v)
	}
	f, err := n.Float64()
	if err != nil {
		t.Fatalf("%s: %v", ctx, err)
	}
	return f
}

// TestRequestShape asserts the required top-level request fields, per the
// OpenAPI schema: state, model, and a non-empty questions map.
func TestRequestShape(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			for _, k := range []string{"state", "model", "questions"} {
				if _, ok := f.Request[k]; !ok {
					t.Errorf("request is missing required field %q", k)
				}
			}
			qs, ok := f.Request["questions"].(map[string]any)
			if !ok {
				t.Fatalf("questions is %T, want object", f.Request["questions"])
			}
			if len(qs) == 0 {
				t.Error("questions is empty; the schema requires minProperties: 1")
			}
			for id, raw := range qs {
				q, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("question %q is %T, want object", id, raw)
				}
				switch q["type"] {
				case "noul":
					// criteria is optional; instructions is optional and nullable.
				case "choice":
					c, ok := q["criteria"].(map[string]any)
					if !ok {
						t.Errorf("question %q: choice criteria must be an object, got %T", id, q["criteria"])
					} else if len(c) == 0 {
						t.Errorf("question %q: choice criteria is empty", id)
					}
				case "score":
					c, ok := q["criteria"].([]any)
					if !ok {
						t.Errorf("question %q: score criteria must be an ordered array, got %T", id, q["criteria"])
					} else if len(c) < 1 {
						t.Errorf("question %q: score criteria is empty", id)
					}
				default:
					t.Errorf("question %q: unknown type %v", id, q["type"])
				}
			}
		})
	}
}

// TestEveryQuestionIsAnswered asserts the response keys the answers by the
// same ids the caller chose.
func TestEveryQuestionIsAnswered(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			qs := f.Request["questions"].(map[string]any)
			as, ok := f.Response["answers"].(map[string]any)
			if !ok {
				t.Fatalf("answers is %T, want object", f.Response["answers"])
			}
			for id := range qs {
				a, ok := as[id]
				if !ok {
					t.Errorf("question %q has no answer", id)
					continue
				}
				am := a.(map[string]any)
				if am["type"] != qs[id].(map[string]any)["type"] {
					t.Errorf("question %q: answer type %v does not match question type %v",
						id, am["type"], qs[id].(map[string]any)["type"])
				}
			}
			for id := range as {
				if _, ok := qs[id]; !ok {
					t.Errorf("answer %q corresponds to no question", id)
				}
			}
		})
	}
}

// TestNoulAnswerHasNoConfidence guards correction C2. Noul answers carry only
// type and noul: the probability itself expresses the uncertainty. Any helper
// that reads a Confidence off a Noul is reading a zero that means nothing.
func TestNoulAnswerHasNoConfidence(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			for id, raw := range f.Response["answers"].(map[string]any) {
				a := raw.(map[string]any)
				if a["type"] != "noul" {
					continue
				}
				if _, bad := a["confidence"]; bad {
					t.Errorf("answer %q: noul answers must not carry a confidence field", id)
				}
				if v := num(t, a["noul"], id); v < 0 || v > 1 {
					t.Errorf("answer %q: noul = %v, want within [0,1]", id, v)
				}
				for k := range a {
					if k != "type" && k != "noul" {
						t.Errorf("answer %q: unexpected field %q on a noul answer", id, k)
					}
				}
			}
		})
	}
}

// TestChoiceAnswerInvariants asserts the distribution sums to 1 and that the
// reported choice is the highest-probability option.
func TestChoiceAnswerInvariants(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			qs := f.Request["questions"].(map[string]any)
			for id, raw := range f.Response["answers"].(map[string]any) {
				a := raw.(map[string]any)
				if a["type"] != "choice" {
					continue
				}
				probs := a["probabilities"].(map[string]any)

				sum, best, bestP := 0.0, "", math.Inf(-1)
				for opt, pv := range probs {
					p := num(t, pv, id+"."+opt)
					if p < 0 || p > 1 {
						t.Errorf("answer %q: probability for %q is %v, want within [0,1]", id, opt, p)
					}
					sum += p
					if p > bestP {
						best, bestP = opt, p
					}
				}
				if math.Abs(sum-1) > 1e-3 {
					t.Errorf("answer %q: probabilities sum to %v, want 1", id, sum)
				}
				if a["choice"] != best {
					t.Errorf("answer %q: choice is %v but %q has the highest probability (%v)",
						id, a["choice"], best, bestP)
				}
				if c := num(t, a["confidence"], id); c < 0 || c > 1 {
					t.Errorf("answer %q: confidence = %v, want within [0,1]", id, c)
				}

				// Every option offered must appear in the distribution, and
				// nothing else may.
				crit := qs[id].(map[string]any)["criteria"].(map[string]any)
				if len(crit) != len(probs) {
					t.Errorf("answer %q: %d options offered but %d probabilities returned",
						id, len(crit), len(probs))
				}
				for opt := range crit {
					if _, ok := probs[opt]; !ok {
						t.Errorf("answer %q: option %q has no probability", id, opt)
					}
				}
			}
		})
	}
}

// TestScoreAnswerInvariants asserts that legend and probabilities agree, that
// their keys are numeric strings covering 0..n-1, and that the reported score
// is the probability-weighted mean of the level indices.
func TestScoreAnswerInvariants(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			qs := f.Request["questions"].(map[string]any)
			for id, raw := range f.Response["answers"].(map[string]any) {
				a := raw.(map[string]any)
				if a["type"] != "score" {
					continue
				}
				legend := a["legend"].(map[string]any)
				probs := a["probabilities"].(map[string]any)

				levels := qs[id].(map[string]any)["criteria"].([]any)
				if len(legend) != len(levels) {
					t.Errorf("answer %q: %d levels requested but legend has %d entries",
						id, len(levels), len(legend))
				}
				if len(probs) != len(legend) {
					t.Errorf("answer %q: legend has %d entries but probabilities has %d",
						id, len(legend), len(probs))
				}

				// Keys must be the numeric strings 0..n-1, exactly once each.
				seen := make(map[int]bool, len(legend))
				for k := range legend {
					i, err := strconv.Atoi(k)
					if err != nil {
						t.Errorf("answer %q: legend key %q is not a numeric string", id, k)
						continue
					}
					if i < 0 || i >= len(legend) {
						t.Errorf("answer %q: legend key %q is outside 0..%d", id, k, len(legend)-1)
					}
					seen[i] = true
					if _, ok := probs[k]; !ok {
						t.Errorf("answer %q: legend has level %q but probabilities does not", id, k)
					}
				}
				for i := range len(legend) {
					if !seen[i] {
						t.Errorf("answer %q: level %d missing from legend", id, i)
					}
				}

				sum, weighted := 0.0, 0.0
				for k, pv := range probs {
					p := num(t, pv, id+"."+k)
					i, err := strconv.Atoi(k)
					if err != nil {
						continue
					}
					sum += p
					weighted += float64(i) * p
				}
				if math.Abs(sum-1) > 1e-3 {
					t.Errorf("answer %q: probabilities sum to %v, want 1", id, sum)
				}
				got := num(t, a["score"], id)
				if math.Abs(got-weighted) > 1e-3 {
					t.Errorf("answer %q: score = %v but the probability-weighted mean is %v",
						id, got, weighted)
				}
				if got < 0 || got > float64(len(legend)-1) {
					t.Errorf("answer %q: score = %v, want within [0,%d]", id, got, len(legend)-1)
				}
			}
		})
	}
}

// TestScoreLevelKeysAreContiguousIntegers asserts that legend and probability
// keys are the numeric strings 0..n-1 with no gaps.
//
// A note on the string-sort trap, because an earlier revision of this file
// overstated it: legend keys are strings holding integers, so sorting them
// lexicographically would put "10" between "1" and "2". That divergence is
// real in principle and **currently unreachable in practice** — the API caps a
// Score at ten levels (400: "Too many score levels. Must have at most 10
// levels."), so indices never leave 0..9, where string order and numeric order
// coincide.
//
// The SDK still parses keys as integers rather than sorting them as strings.
// That is defensive, not a bug fix: it costs nothing, and it is what keeps the
// ordered accessors correct if TypeSafe ever raises the cap. No claim should be
// made that other clients are broken here — against today's server they are not.
func TestScoreLevelKeysAreContiguousIntegers(t *testing.T) {
	var checked int
	for _, f := range loadFixtures(t) {
		for id, raw := range f.Response["answers"].(map[string]any) {
			a := raw.(map[string]any)
			if a["type"] != "score" {
				continue
			}
			legend := a["legend"].(map[string]any)
			checked++

			if len(legend) > maxScoreLevels {
				t.Errorf("fixture %s answer %q: %d levels exceeds the server maximum of %d",
					f.Name, id, len(legend), maxScoreLevels)
			}

			seen := make([]bool, len(legend))
			for k := range legend {
				i, err := strconv.Atoi(k)
				if err != nil {
					t.Errorf("fixture %s answer %q: legend key %q is not an integer string",
						f.Name, id, k)
					continue
				}
				if i < 0 || i >= len(legend) {
					t.Errorf("fixture %s answer %q: level %d outside 0..%d",
						f.Name, id, i, len(legend)-1)
					continue
				}
				if seen[i] {
					t.Errorf("fixture %s answer %q: level %d appears twice", f.Name, id, i)
				}
				seen[i] = true
			}
			for i, ok := range seen {
				if !ok {
					t.Errorf("fixture %s answer %q: level %d missing", f.Name, id, i)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no score fixtures found")
	}
}

// TestScoreLevelBoundsAreCovered keeps fixtures at both ends of the legal
// rubric range, since both were wrong in an earlier revision: the prose docs
// claim a two-level minimum (the server accepts one) and neither the docs nor
// the OpenAPI schema mentions the ten-level maximum at all.
func TestScoreLevelBoundsAreCovered(t *testing.T) {
	var sawMin, sawMax bool
	for _, f := range loadFixtures(t) {
		for _, raw := range f.Request["questions"].(map[string]any) {
			q := raw.(map[string]any)
			if q["type"] != "score" {
				continue
			}
			switch n := len(q["criteria"].([]any)); n {
			case 1:
				sawMin = true
			case maxScoreLevels:
				sawMax = true
			}
		}
	}
	if !sawMin {
		t.Error("no fixture exercises a single-level Score (the true minimum)")
	}
	if !sawMax {
		t.Errorf("no fixture exercises a %d-level Score (the server maximum)", maxScoreLevels)
	}
}

// TestNullCriteriaSurviveRoundTrip guards correction C6's sibling: a Choice
// option with no description must marshal to JSON null, not "" and not an
// omitted key. The fixture is the canary; Phase 2 must reproduce it byte for
// byte.
func TestNullCriteriaSurviveRoundTrip(t *testing.T) {
	var found bool
	for _, f := range loadFixtures(t) {
		for id, raw := range f.Request["questions"].(map[string]any) {
			q := raw.(map[string]any)
			crit, ok := q["criteria"].(map[string]any)
			if !ok {
				continue
			}
			for opt, desc := range crit {
				if desc != nil {
					continue
				}
				found = true
				// Re-marshal and confirm the null is still a null.
				b, err := json.Marshal(crit)
				if err != nil {
					t.Fatalf("marshal %s/%s: %v", f.Name, id, err)
				}
				want := `"` + opt + `":null`
				if !strings.Contains(string(b), want) {
					t.Errorf("fixture %s question %q: option %q lost its null on re-marshal: %s",
						f.Name, id, opt, b)
				}
			}
		}
	}
	if !found {
		t.Fatal("no fixture exercises null criteria values")
	}
}

// TestUsageIsPresent asserts token accounting is reported on every response.
func TestUsageIsPresent(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			u, ok := f.Response["usage"].(map[string]any)
			if !ok {
				t.Fatalf("usage is %T, want object", f.Response["usage"])
			}
			for _, k := range []string{"input_tokens", "output_tokens"} {
				v, ok := u[k]
				if !ok {
					t.Errorf("usage is missing %q", k)
					continue
				}
				if n := num(t, v, "usage."+k); n < 0 || n != math.Trunc(n) {
					t.Errorf("usage.%s = %v, want a non-negative integer", k, n)
				}
			}
			if _, ok := f.Response["model"].(string); !ok {
				t.Error("response is missing the model that answered")
			}
		})
	}
}
