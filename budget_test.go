package typesafe_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
)

func bigString(n int) string { return strings.Repeat("word ", n/5) }

// TestEstimateAccountsForFixedOverhead is the finding that motivated measuring
// rather than guessing: a request whose content is 31 characters still costs
// 283 input tokens. An estimator that scaled purely with content would be off
// by an order of magnitude on a batch of small requests.
func TestEstimateAccountsForFixedOverhead(t *testing.T) {
	tiny := &typesafe.SystemOneRequest{
		State:     "hello",
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{Instructions: "?"}},
	}
	est := tiny.EstimateTokens()

	if est.Overhead < 200 {
		t.Errorf("Overhead = %d; measurement showed ~250 tokens fixed per call", est.Overhead)
	}
	if est.Total < 250 {
		t.Errorf("Total = %d for a tiny request; the fixed cost dominates here", est.Total)
	}
	if !est.Approximate {
		t.Error("Approximate must always be true")
	}
}

// TestEstimateNeverUnderReportsOnMeasuredCorpus pins the model against the
// real token counts recorded from the live API. These are measurements, not
// guesses: if the model drifts below any of them it will fail a request check
// exactly when the request is near a limit.
func TestEstimateNeverUnderReportsOnMeasuredCorpus(t *testing.T) {
	// Recorded from api.typesafe.ai over the golden contract corpus.
	measured := []struct {
		name   string
		state  any
		qs     map[string]typesafe.Question
		actual int
	}{
		{"noul_single", "Help! My payouts have been failing for 3 days.",
			map[string]typesafe.Question{"is_urgent": typesafe.Noul{
				Instructions: "Does this convey urgency?",
				Criteria: &typesafe.NoulCriteria{
					True: "Explicitly time-sensitive", False: "No urgency expressed"},
			}}, 307},
		{"choice_single", "Help! My payouts have been failing for 3 days.",
			map[string]typesafe.Question{"department": typesafe.Choice{
				Instructions: "Which team should handle this?",
				Criteria: typesafe.Options{
					"billing":   "Payments, invoicing, refunds",
					"technical": "Bugs, outages, integrations",
					"sales":     "Pricing, upgrades, new accounts"},
			}}, 349},
		{"score_single", "Help! My payouts have been failing for 3 days.",
			map[string]typesafe.Question{"frustration": typesafe.Score{
				Instructions: "How frustrated is the customer?",
				Criteria:     typesafe.Levels{"Calm", "Frustrated", "Very angry"},
			}}, 312},
		{"score_single_level", "hello",
			map[string]typesafe.Question{"q": typesafe.Score{
				Instructions: "Rate it.", Criteria: typesafe.Levels{"only level"},
			}}, 283},
	}

	for _, m := range measured {
		t.Run(m.name, func(t *testing.T) {
			est := (&typesafe.SystemOneRequest{State: m.state, Questions: m.qs}).EstimateTokens()
			if est.Total < m.actual {
				t.Errorf("estimated %d tokens but the API charged %d — the estimate must "+
					"never fall below the truth", est.Total, m.actual)
			}
			ratio := float64(est.Total) / float64(m.actual)
			if ratio > 1.35 {
				t.Errorf("estimated %d against an actual %d (%.2fx); over-reporting this "+
					"much makes the check useless", est.Total, m.actual, ratio)
			}
			t.Logf("estimate %d, actual %d (%.2fx)", est.Total, m.actual, ratio)
		})
	}
}

// TestSingleQuestionLimitIsSeparate is the trap Phase 7 exists for.
//
// The API enforces two independent ceilings: 64k for the whole request, and
// 32k for the state plus any single question. A request can sit comfortably
// inside the first and still be rejected by the second, and the server's error
// does not say which one it hit.
//
// Sizing the fixture: at roughly 0.41 tokens per wire byte, an 80KB state puts
// state-plus-question near 33k tokens — over the 32k single-question ceiling,
// and less than half the 64k whole-request one.
func TestSingleQuestionLimitIsSeparate(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State: bigString(80_000),
		Questions: map[string]typesafe.Question{
			"big":   typesafe.Noul{Instructions: "Does this long document discuss refunds?"},
			"small": typesafe.Noul{Instructions: "Urgent?"},
		},
	}
	est := req.EstimateTokens()

	if !est.ExceedsSingle {
		t.Fatalf("expected the single-question ceiling to be exceeded:\n%s", est)
	}
	if est.ExceedsTotal {
		t.Fatalf("this fixture should stay under the whole-request ceiling, "+
			"or it does not exercise the separate-limit case:\n%s", est)
	}
	t.Logf("single %d / %d, total %d / %d — inside one limit, outside the other",
		est.LongestSingle, typesafe.MaxSingleQuestionTokens,
		est.Total, typesafe.MaxContextTokens)

	err := est.Err()
	if !errors.Is(err, typesafe.ErrInvalidRequest) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
	// The message has to distinguish the two ceilings, or the reader is left
	// counting bytes by hand.
	for _, want := range []string{"separate ceiling", "32000", "64000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got:\n%v", want, err)
		}
	}
	// And name which question is responsible.
	if est.LongestQuestionID != "big" {
		t.Errorf("LongestQuestionID = %q, want the larger question", est.LongestQuestionID)
	}
}

func TestTotalLimitIsChecked(t *testing.T) {
	qs := map[string]typesafe.Question{}
	for i := range 40 {
		qs[string(rune('a'+i%26))+string(rune('0'+i/26))] = typesafe.Noul{
			Instructions: bigString(8_000),
		}
	}
	est := (&typesafe.SystemOneRequest{State: "x", Questions: qs}).EstimateTokens()
	if !est.ExceedsTotal {
		t.Fatalf("expected the total limit to be exceeded, got %d tokens", est.Total)
	}
	if !strings.Contains(est.Err().Error(), "all questions") {
		t.Errorf("err = %v, want it to name the whole-request limit", est.Err())
	}
}

func TestClientRefusesOversizeRequestsWithoutSending(t *testing.T) {
	var reached bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeJSON(t, w, 200, okResponse())
	})

	req := &typesafe.SystemOneRequest{
		State:     bigString(400_000),
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{Instructions: "?"}},
	}
	_, err := c.SystemOne(context.Background(), req)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if reached {
		t.Error("the request reached the server; an over-limit request must cost nothing")
	}
}

// TestContextCheckCanBeDisabled: the estimate over-reports by design, so a
// caller who would rather let the server decide must be able to say so.
func TestContextCheckCanBeDisabled(t *testing.T) {
	var reached bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithContextLimitCheck(false))

	req := &typesafe.SystemOneRequest{
		State:     bigString(400_000),
		Questions: map[string]typesafe.Question{"is_urgent": typesafe.Noul{Instructions: "?"}},
	}
	if _, err := c.SystemOne(context.Background(), req); err != nil {
		t.Fatalf("with the check off the request should be attempted: %v", err)
	}
	if !reached {
		t.Error("the request did not reach the server")
	}
}

func TestEstimateCost(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State:     "a support ticket",
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{Instructions: "urgent?"}},
	}

	cost := req.EstimateCost(0)
	if cost.RatePerMillionUSD != typesafe.DefaultInputCostPerMillionTokens {
		t.Errorf("rate = %v, want the published default", cost.RatePerMillionUSD)
	}
	if cost.InputCostUSD <= 0 {
		t.Error("cost should be positive")
	}
	if cost.Questions != 1 {
		t.Errorf("Questions = %d", cost.Questions)
	}

	// A negotiated rate must be usable; a hard-coded price is wrong the
	// moment anyone's contract differs.
	custom := req.EstimateCost(0.02)
	if custom.RatePerMillionUSD != 0.02 {
		t.Errorf("custom rate = %v", custom.RatePerMillionUSD)
	}
	if custom.InputCostUSD >= cost.InputCostUSD {
		t.Error("a cheaper rate should produce a lower estimate")
	}

	// It must never read as authoritative.
	if !strings.Contains(cost.String(), "approximate") {
		t.Errorf("cost output must be flagged approximate, got: %s", cost)
	}
}

func TestEstimateHandlesNilRequest(t *testing.T) {
	var req *typesafe.SystemOneRequest
	est := req.EstimateTokens()
	if !est.Approximate {
		t.Error("Approximate should still be true")
	}
	if est.Err() != nil {
		t.Errorf("a nil request should not report a limit breach: %v", est.Err())
	}
	if c := req.EstimateCost(0); c.Questions != 0 {
		t.Errorf("Questions = %d for a nil request", c.Questions)
	}
}

func TestEstimateStringIsReadable(t *testing.T) {
	req := &typesafe.SystemOneRequest{
		State: "ticket text",
		Questions: map[string]typesafe.Question{
			"short": typesafe.Noul{Instructions: "?"},
			"long":  typesafe.Noul{Instructions: bigString(2000)},
		},
	}
	s := req.EstimateTokens().String()
	for _, want := range []string{"approximate", "fixed overhead", "state", "long", "total"} {
		if !strings.Contains(s, want) {
			t.Errorf("estimate output missing %q:\n%s", want, s)
		}
	}
	// Questions are listed largest first.
	if strings.Index(s, "long") > strings.Index(s, "short") {
		t.Errorf("questions should be listed by descending cost:\n%s", s)
	}
}

// --- budget ------------------------------------------------------------------

func TestBudgetRefusesBeforeAnyNetworkIO(t *testing.T) {
	var reached bool
	budget := typesafe.NewBudget(typesafe.MaxTotalRequests(2))
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithBudget(budget))

	for i := range 2 {
		if _, err := c.SystemOne(context.Background(), sampleRequest()); err != nil {
			t.Fatalf("call %d should have been allowed: %v", i, err)
		}
	}

	reached = false
	_, err := c.SystemOne(context.Background(), sampleRequest())
	if !errors.Is(err, typesafe.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if reached {
		t.Error("an over-budget request reached the server; it must cost nothing")
	}
	if !strings.Contains(err.Error(), "lifetime cap") {
		t.Errorf("err = %v, want it to name which cap was hit", err)
	}
}

func TestBudgetRequestRate(t *testing.T) {
	b := typesafe.NewBudget(typesafe.MaxRequestsPerMinute(3))
	for i := range 3 {
		if err := typesafe.BudgetCheckForTest(b, 10); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	err := typesafe.BudgetCheckForTest(b, 10)
	if !errors.Is(err, typesafe.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	// The message should say when capacity returns, not just that it is gone.
	if !strings.Contains(err.Error(), "frees up in") {
		t.Errorf("err = %v, want it to say when to retry", err)
	}

	if u := b.Usage(); u.RequestsLastMinute != 3 || u.TotalRequests != 3 {
		t.Errorf("Usage = %+v", u)
	}
	b.Reset()
	if u := b.Usage(); u.TotalRequests != 0 {
		t.Errorf("after Reset: %+v", u)
	}
}

func TestBudgetTokenRate(t *testing.T) {
	b := typesafe.NewBudget(typesafe.MaxTokensPerSecond(1000))
	if err := typesafe.BudgetCheckForTest(b, 600); err != nil {
		t.Fatalf("first: %v", err)
	}
	// 600 + 600 exceeds 1000, so the second must be refused.
	err := typesafe.BudgetCheckForTest(b, 600)
	if !errors.Is(err, typesafe.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if !strings.Contains(err.Error(), "per second") {
		t.Errorf("err = %v, want it to name the token-rate cap", err)
	}
	// A smaller request still fits in the remaining headroom.
	if err := typesafe.BudgetCheckForTest(b, 300); err != nil {
		t.Errorf("a request within the remaining headroom should pass: %v", err)
	}
}

func TestBudgetTotalTokens(t *testing.T) {
	b := typesafe.NewBudget(typesafe.MaxTotalTokens(1000))
	if err := typesafe.BudgetCheckForTest(b, 900); err != nil {
		t.Fatalf("first: %v", err)
	}
	err := typesafe.BudgetCheckForTest(b, 200)
	if !errors.Is(err, typesafe.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if !strings.Contains(err.Error(), "lifetime cap") {
		t.Errorf("err = %v", err)
	}
}

func TestBudgetWithNoLimitsAllowsEverything(t *testing.T) {
	b := typesafe.NewBudget()
	for i := range 5000 {
		if err := typesafe.BudgetCheckForTest(b, 1000); err != nil {
			t.Fatalf("request %d refused by a budget with no limits: %v", i, err)
		}
	}
}

// TestBudgetIsRaceFree: a hundred goroutines must not each observe room for
// the last request. Reservation and accounting happen under one lock.
func TestBudgetIsRaceFree(t *testing.T) {
	const cap = 50
	b := typesafe.NewBudget(typesafe.MaxTotalRequests(cap))

	var mu sync.Mutex
	var allowed int
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := typesafe.BudgetCheckForTest(b, 1); err == nil {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != cap {
		t.Errorf("%d requests were allowed against a cap of %d; the reservation is racy",
			allowed, cap)
	}
	if u := b.Usage(); u.TotalRequests != cap {
		t.Errorf("Usage.TotalRequests = %d, want %d", u.TotalRequests, cap)
	}
}

func TestDefaultBudgetUsesPublishedLimits(t *testing.T) {
	b := typesafe.DefaultBudget()
	// Enough requests to exceed the published per-minute cap.
	var refused bool
	for range typesafe.DefaultRequestsPerMinute + 10 {
		if err := typesafe.BudgetCheckForTest(b, 1); err != nil {
			refused = true
			break
		}
	}
	if !refused {
		t.Errorf("DefaultBudget did not enforce the published %d req/min limit",
			typesafe.DefaultRequestsPerMinute)
	}
}

func TestWithBudgetRejectsNil(t *testing.T) {
	if _, err := typesafe.NewClient(typesafe.WithAPIKey("k"), typesafe.WithBudget(nil)); err == nil {
		t.Error("a nil budget should be rejected")
	}
}

// TestBudgetIsSharedAcrossClients: the account has the quota, not the client.
func TestBudgetIsSharedAcrossClients(t *testing.T) {
	budget := typesafe.NewBudget(typesafe.MaxTotalRequests(3))

	c1 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithBudget(budget))
	c2 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, okResponse())
	}, typesafe.WithBudget(budget))

	for i, c := range []*typesafe.Client{c1, c2, c1} {
		if _, err := c.SystemOne(context.Background(), sampleRequest()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := c2.SystemOne(context.Background(), sampleRequest()); !errors.Is(err, typesafe.ErrBudgetExceeded) {
		t.Errorf("err = %v; a shared budget must account across clients", err)
	}
}
