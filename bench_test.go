package typesafe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
)

// Benchmarks for PERFORMANCE.md.
//
// # Why not testing.B.Loop
//
// B.Loop arrived in Go 1.24 and this module declares a 1.23 floor, which is a
// published promise. The classic b.N loop measures the same thing; it just
// needs b.ResetTimer where setup is involved, which is done below.
//
// # What these measure, and what they cannot
//
// Every benchmark here runs against an in-process httptest server, so the
// numbers are SDK overhead and loopback HTTP — not the API. The real call is
// documented at 70–500ms, which is two to three orders of magnitude larger
// than anything measured here. That is the point: the figures exist to prove
// the SDK is not the bottleneck, not to suggest it could be.

// benchBody is a realistic three-answer response.
func benchBody() []byte {
	b, err := json.Marshal(map[string]any{
		"model": "jev-1.13.0",
		"answers": map[string]any{
			"is_urgent": map[string]any{"type": "noul", "noul": 0.92},
			"team": map[string]any{
				"type": "choice", "choice": "billing", "confidence": 0.81,
				"probabilities": map[string]float64{
					"billing": 0.81, "technical": 0.12, "sales": 0.07,
				},
			},
			"severity": map[string]any{
				"type": "score", "score": 2.38, "confidence": 0.62,
				"legend": map[string]any{
					"0": "No impact", "1": "Minor", "2": "Blocking", "3": "Unusable",
				},
				"probabilities": map[string]float64{
					"0": 0.05, "1": 0.15, "2": 0.55, "3": 0.25,
				},
			},
		},
		"usage": map[string]any{"input_tokens": 312, "output_tokens": 48},
	})
	if err != nil {
		panic(err)
	}
	return b
}

func benchServer(b *testing.B) *httptest.Server {
	b.Helper()
	body := benchBody()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the request so the connection is reusable, exactly as a real
		// server would.
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	b.Cleanup(srv.Close)
	return srv
}

func benchRequest() *typesafe.SystemOneRequest {
	return &typesafe.SystemOneRequest{
		State: "My card was declined twice on renewal and I was charged anyway. " +
			"The billing page also returns a 500 error.",
		Model: "jev-latest",
		Questions: typesafe.Questions{
			"is_urgent": typesafe.Noul{
				Instructions: "Does this message convey urgency?",
				Criteria: &typesafe.NoulCriteria{
					True: "Needs a response today", False: "Can wait",
				},
			},
			"team": typesafe.Choice{
				Instructions: "Which team should handle this?",
				Criteria: typesafe.Options{
					"billing":   "Payments, invoicing, refunds",
					"technical": "Bugs, outages, integrations",
					"sales":     "Pricing, upgrades, new accounts",
				},
			},
			"severity": typesafe.Score{
				Instructions: "How severe is the problem described here?",
				Criteria:     typesafe.Levels{"No impact", "Minor", "Blocking", "Unusable"},
			},
		},
	}
}

// --- the headline pair -------------------------------------------------------

// BenchmarkSystemOne is one full client call: validation, token estimate,
// marshalling, the round trip, and decoding.
func BenchmarkSystemOne(b *testing.B) {
	srv := benchServer(b)
	c, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-bench"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
	)
	if err != nil {
		b.Fatal(err)
	}
	req := benchRequest()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := c.SystemOne(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		if resp.Model == "" {
			b.Fatal("empty response")
		}
	}
}

// BenchmarkRawHTTPBaseline is the same request with nothing but net/http and
// encoding/json — no validation, no typed errors, no estimate, no decode.
//
// The gap between this and BenchmarkSystemOne is what the SDK costs. Quoting
// BenchmarkSystemOne alone would attribute loopback HTTP and JSON to the SDK,
// which is most of it.
func BenchmarkRawHTTPBaseline(b *testing.B) {
	srv := benchServer(b)
	req := benchRequest()

	body, err := json.Marshal(map[string]any{
		"state": req.State, "model": req.Model, "questions": req.Questions,
	})
	if err != nil {
		b.Fatal(err)
	}
	hc := srv.Client()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			srv.URL+typesafe.SystemOnePath, bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer sk-bench")

		resp, err := hc.Do(httpReq)
		if err != nil {
			b.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			b.Fatal(err)
		}
		var out typesafe.SystemOneResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// --- the pieces --------------------------------------------------------------

func BenchmarkMarshalRequest(b *testing.B) {
	req := benchRequest()
	wire := struct {
		State     any                `json:"state"`
		Model     string             `json:"model"`
		Questions typesafe.Questions `json:"questions"`
	}{req.State, req.Model, req.Questions}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(wire); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidate(b *testing.B) {
	req := benchRequest()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := req.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEstimateTokens(b *testing.B) {
	req := benchRequest()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if est := req.EstimateTokens(); est.Total == 0 {
			b.Fatal("no estimate")
		}
	}
}

// Decoding one answer out of a response, which is what a caller does per
// question on every call.
func BenchmarkDecodeAnswers(b *testing.B) {
	var resp typesafe.SystemOneResponse
	if err := json.Unmarshal(benchBody(), &resp); err != nil {
		b.Fatal(err)
	}

	b.Run("noul", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := resp.Noul("is_urgent"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("choice", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := resp.Choice("team"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("score", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := resp.Score("severity"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("all", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := resp.All(); len(got) != 3 {
				b.Fatal("wrong answer count")
			}
		}
	})
}

// Ranked and AtOrAbove are the accessors a routing decision calls, so their
// cost lands on every request rather than once per process.
func BenchmarkAnswerAccessors(b *testing.B) {
	var resp typesafe.SystemOneResponse
	if err := json.Unmarshal(benchBody(), &resp); err != nil {
		b.Fatal(err)
	}
	choice, err := resp.Choice("team")
	if err != nil {
		b.Fatal(err)
	}
	score, err := resp.Score("severity")
	if err != nil {
		b.Fatal(err)
	}

	b.Run("Ranked", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := choice.Ranked(); len(got) != 3 {
				b.Fatal("wrong length")
			}
		}
	})
	b.Run("Margin", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = choice.Margin()
		}
	})
	b.Run("AtOrAbove", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = score.AtOrAbove(2)
		}
	})
}

// Batching is the path where SDK overhead could actually matter, because it
// runs once per state rather than once per process.
func BenchmarkSystemOneBatch(b *testing.B) {
	srv := benchServer(b)
	c, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-bench"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
	)
	if err != nil {
		b.Fatal(err)
	}

	const n = 100
	states := make([]any, n)
	for i := range states {
		states[i] = "ticket text for benchmarking, item number one hundred"
	}
	qs := benchRequest().Questions
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := c.SystemOneBatch(ctx, states, qs, typesafe.WithConcurrency(16))
		if result.Failed != 0 {
			b.Fatalf("%d items failed", result.Failed)
		}
	}
	// Per-item is the number that matters for a batch.
	b.ReportMetric(float64(n), "items/op")
}

// --- SDK overhead, measured without the network ------------------------------

// stubTransport answers every request from memory.
//
// The loopback pair above is realistic but noisy: a round trip over TCP costs
// ~136us with a standard deviation larger than the SDK overhead being
// measured, so subtracting one from the other yields a number dominated by
// variance. Removing the socket removes the variance, and what is left is the
// SDK's own work.
type stubTransport struct{ body []byte }

func (t stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	h := make(http.Header, 1)
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(t.body)),
		Request:    r,
	}, nil
}

// BenchmarkSystemOneNoNetwork is the full client path with the socket removed.
func BenchmarkSystemOneNoNetwork(b *testing.B) {
	c, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-bench"),
		typesafe.WithBaseURL("https://bench.invalid"),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		typesafe.WithHTTPClient(&http.Client{Transport: stubTransport{benchBody()}}),
	)
	if err != nil {
		b.Fatal(err)
	}
	req := benchRequest()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.SystemOne(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRawNoNetwork is marshal, transport, unmarshal — the irreducible
// work of making this call at all, through the same stub.
//
// BenchmarkSystemOneNoNetwork minus this is the SDK's overhead per call.
func BenchmarkRawNoNetwork(b *testing.B) {
	req := benchRequest()
	body, err := json.Marshal(map[string]any{
		"state": req.State, "model": req.Model, "questions": req.Questions,
	})
	if err != nil {
		b.Fatal(err)
	}
	hc := &http.Client{Transport: stubTransport{benchBody()}}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://bench.invalid"+typesafe.SystemOnePath, bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer sk-bench")

		resp, err := hc.Do(httpReq)
		if err != nil {
			b.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			b.Fatal(err)
		}
		var out typesafe.SystemOneResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSystemOneNoPreflight isolates the cost of the pre-flight checks
// that run on every call: validation and the token estimate.
//
// WithContextLimitCheck(false) and no budget means the estimate is not needed
// for anything, which is the comparison that shows what it costs.
func BenchmarkSystemOneNoPreflight(b *testing.B) {
	c, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-bench"),
		typesafe.WithBaseURL("https://bench.invalid"),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		typesafe.WithContextLimitCheck(false),
		typesafe.WithHTTPClient(&http.Client{Transport: stubTransport{benchBody()}}),
	)
	if err != nil {
		b.Fatal(err)
	}
	req := benchRequest()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.SystemOne(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}
