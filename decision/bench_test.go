package decision_test

import (
	"encoding/json"
	"fmt"
	"runtime"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/decision"
)

// Benchmarks for PERFORMANCE.md.
//
// Everything here runs per response, on the caller's goroutine, after the API
// has already answered. The bar is not "fast" but "invisible next to a 70ms
// call" — and the one place that could fail the bar is the Poisson-binomial
// distribution in AtLeast, whose cost grows with the square of the number of
// terms.

func benchResponse(b *testing.B, n int) *typesafe.SystemOneResponse {
	b.Helper()
	answers := make(map[string]json.RawMessage, n)
	for i := 0; i < n; i++ {
		answers[fmt.Sprintf("q%d", i)] = json.RawMessage(
			fmt.Sprintf(`{"type":"noul","noul":%.3f}`, 0.1+float64(i%9)*0.1))
	}
	return &typesafe.SystemOneResponse{Model: "jev-1.13.0", Answers: answers}
}

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("q%d", i)
	}
	return out
}

// probs reads the probabilities a decision is composed from, which is what a
// caller does before calling into the algebra.
func probs(b *testing.B, src decision.Source, names []string) []float64 {
	b.Helper()
	out := make([]float64, 0, len(names))
	for _, id := range names {
		a, err := src.Noul(id)
		if err != nil {
			b.Fatal(err)
		}
		out = append(out, a.Noul)
	}
	return out
}

// Reading the probabilities out of a response, which happens once per
// decision before any of the algebra runs.
func BenchmarkReadProbabilities(b *testing.B) {
	for _, n := range []int{3, 10, 30} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			resp := benchResponse(b, n)
			names := ids(n)
			b.ReportAllocs()
			b.ResetTimer()
			// sink defeats dead-store elimination. Without it the appended
			// slice is never read, the compiler is free to drop the work, and
			// the benchmark reports the cost of an empty loop. staticcheck
			// flagged exactly that (SA4010) in the first version of this file.
			var sink float64
			for i := 0; i < b.N; i++ {
				out := make([]float64, 0, len(names))
				for _, id := range names {
					a, err := resp.Noul(id)
					if err != nil {
						b.Fatal(err)
					}
					out = append(out, a.Noul)
				}
				sink += out[len(out)-1]
			}
			runtime.KeepAlive(sink)
		})
	}
}

func BenchmarkAlgebra(b *testing.B) {
	resp := benchResponse(b, 10)
	vals := probs(b, resp, ids(10))

	b.Run("All", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = decision.All(vals...)
		}
	})
	b.Run("Any", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = decision.Any(vals...)
		}
	})
	b.Run("Expected", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = decision.Expected(vals...)
		}
	})
}

// AtLeast builds a Poisson-binomial distribution, so its cost is quadratic in
// the number of terms. This is the one accessor where a large question set
// could stop being free, and the numbers say where that starts.
func BenchmarkAtLeast(b *testing.B) {
	for _, n := range []int{3, 10, 30, 100} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			resp := benchResponse(b, n)
			vals := probs(b, resp, ids(n))
			k := n / 2

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = decision.AtLeast(k, vals...)
			}
		})
	}
}

func BenchmarkPolicyEvaluate(b *testing.B) {
	for _, n := range []int{3, 10, 30} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			resp := benchResponse(b, n)
			weights := make(decision.Weights, n)
			for i := 0; i < n; i++ {
				weights[fmt.Sprintf("q%d", i)] = float64(i%3) + 1
			}
			p := decision.NewPolicy("bench", weights)
			p.ReviewAbove = 0.5
			p.BlockAbove = 0.9

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := p.Evaluate(resp); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
