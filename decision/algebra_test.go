package decision_test

import (
	"math"
	"math/rand/v2"
	"testing"
	"testing/quick"

	"github.com/nibir1/typesafe-go/decision"
)

const tol = 1e-9

// approx fails unless got and want agree within tol.
//
// Named approx rather than close because close is a builtin, and shadowing it
// in a test file is the kind of thing that reads fine until someone needs the
// builtin twenty lines later.
func approx(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestBasicAlgebra(t *testing.T) {
	approx(t, decision.Not(0.92), 0.08, "Not(0.92)")
	approx(t, decision.And(0.9, 0.8), 0.72, "And(0.9, 0.8)")
	approx(t, decision.Or(0.9, 0.8), 0.98, "Or(0.9, 0.8)")
	approx(t, decision.All(0.5, 0.5, 0.5), 0.125, "All(0.5 x3)")
	approx(t, decision.Any(0.5, 0.5, 0.5), 0.875, "Any(0.5 x3)")

	// Identities: nothing to check cannot fail, nothing to find cannot be found.
	approx(t, decision.All(), 1, "All()")
	approx(t, decision.Any(), 0, "Any()")
	approx(t, decision.MinAll(), 1, "MinAll()")
	approx(t, decision.MaxAny(), 0, "MaxAny()")
}

// TestOrIsNotAddition guards the most common way to produce a "probability"
// above 1.
func TestOrIsNotAddition(t *testing.T) {
	if got := decision.Or(0.8, 0.8); got > 1 {
		t.Errorf("Or(0.8, 0.8) = %v, which is not a probability", got)
	}
	approx(t, decision.Or(0.8, 0.8), 0.96, "Or(0.8, 0.8)")
}

func TestClamp(t *testing.T) {
	cases := map[float64]float64{
		-1: 0, 0: 0, 0.5: 0.5, 1: 1, 2: 1,
		math.Inf(1): 1, math.Inf(-1): 0,
	}
	for in, want := range cases {
		if got := decision.Clamp(in); got != want {
			t.Errorf("Clamp(%v) = %v, want %v", in, got, want)
		}
	}
	// NaN has no sensible probability; 0 is the safe reading.
	if got := decision.Clamp(math.NaN()); got != 0 {
		t.Errorf("Clamp(NaN) = %v, want 0", got)
	}
}

// TestOutOfRangeInputsAreContained: a bad weight or a hand-built fixture must
// not produce a result outside [0,1] that then flows into a policy and yields
// a plausible-looking but meaningless verdict.
func TestOutOfRangeInputsAreContained(t *testing.T) {
	ops := map[string]float64{
		"And":    decision.And(1.5, -0.5),
		"Or":     decision.Or(1.5, -0.5),
		"All":    decision.All(1.5, -0.5, 2),
		"Any":    decision.Any(1.5, -0.5, 2),
		"Not":    decision.Not(-3),
		"MinAll": decision.MinAll(1.5, -0.5),
		"MaxAny": decision.MaxAny(1.5, -0.5),
	}
	for name, got := range ops {
		if got < 0 || got > 1 {
			t.Errorf("%s produced %v, outside [0,1]", name, got)
		}
	}
}

// --- verification against independent computation ----------------------------

// TestAlgebraMatchesMonteCarlo checks the formulas against simulation rather
// than against themselves. A closed form can be self-consistently wrong; a
// simulation of actual coin flips cannot be wrong in the same way.
func TestAlgebraMatchesMonteCarlo(t *testing.T) {
	const (
		trials = 400_000
		// 400k trials puts the standard error near 0.0008, so 0.01 is a
		// comfortable margin that still catches a real formula error.
		margin = 0.01
	)
	rng := rand.New(rand.NewPCG(0xDECA, 0xF00D))

	cases := [][]float64{
		{0.9, 0.8},
		{0.5, 0.5, 0.5},
		{0.1, 0.2, 0.3, 0.4},
		{0.99, 0.01},
		{0.42, 0.42, 0.42, 0.42, 0.42},
	}

	for _, ps := range cases {
		var all, any, atLeast2, exactly1 int
		for range trials {
			hits := 0
			for _, p := range ps {
				if rng.Float64() < p {
					hits++
				}
			}
			if hits == len(ps) {
				all++
			}
			if hits > 0 {
				any++
			}
			if hits >= 2 {
				atLeast2++
			}
			if hits == 1 {
				exactly1++
			}
		}

		check := func(name string, got float64, hits int) {
			t.Helper()
			want := float64(hits) / trials
			if math.Abs(got-want) > margin {
				t.Errorf("%v: %s = %.5f, simulation says %.5f", ps, name, got, want)
			}
		}
		check("All", decision.All(ps...), all)
		check("Any", decision.Any(ps...), any)
		check("AtLeast(2)", decision.AtLeast(2, ps...), atLeast2)
		check("Exactly(1)", decision.Exactly(1, ps...), exactly1)
	}
}

// TestAtLeastMatchesBruteForce verifies the Poisson-binomial dynamic program
// against exhaustive enumeration over every subset. Exact, not statistical.
func TestAtLeastMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))

	for size := 1; size <= 12; size++ {
		ps := make([]float64, size)
		for i := range ps {
			ps[i] = rng.Float64()
		}

		for n := 0; n <= size+1; n++ {
			// Enumerate all 2^size outcomes and sum the ones with >= n hits.
			var want float64
			for mask := 0; mask < 1<<size; mask++ {
				prob := 1.0
				hits := 0
				for i, p := range ps {
					if mask&(1<<i) != 0 {
						prob *= p
						hits++
					} else {
						prob *= 1 - p
					}
				}
				if hits >= n {
					want += prob
				}
			}
			got := decision.AtLeast(n, ps...)
			if math.Abs(got-want) > 1e-9 {
				t.Fatalf("AtLeast(%d) over %d events = %.12f, brute force = %.12f", n, size, got, want)
			}
		}
	}
}

func TestExactlyMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 17))
	for size := 1; size <= 10; size++ {
		ps := make([]float64, size)
		for i := range ps {
			ps[i] = rng.Float64()
		}
		for n := 0; n <= size; n++ {
			var want float64
			for mask := 0; mask < 1<<size; mask++ {
				prob, hits := 1.0, 0
				for i, p := range ps {
					if mask&(1<<i) != 0 {
						prob *= p
						hits++
					} else {
						prob *= 1 - p
					}
				}
				if hits == n {
					want += prob
				}
			}
			if got := decision.Exactly(n, ps...); math.Abs(got-want) > 1e-9 {
				t.Fatalf("Exactly(%d) over %d = %.12f, want %.12f", n, size, got, want)
			}
		}
	}
}

// TestExactlySumsToOne: the distribution over "how many occurred" must be a
// distribution.
func TestExactlySumsToOne(t *testing.T) {
	rng := rand.New(rand.NewPCG(19, 23))
	for size := 1; size <= 10; size++ {
		ps := make([]float64, size)
		for i := range ps {
			ps[i] = rng.Float64()
		}
		var sum float64
		for n := 0; n <= size; n++ {
			sum += decision.Exactly(n, ps...)
		}
		if math.Abs(sum-1) > 1e-9 {
			t.Errorf("Exactly over %d events sums to %.12f, want 1", size, sum)
		}
	}
}

// --- properties --------------------------------------------------------------

func TestAlgebraProperties(t *testing.T) {
	cfg := &quick.Config{MaxCount: 10_000}

	t.Run("double negation", func(t *testing.T) {
		if err := quick.Check(func(a float64) bool {
			p := decision.Clamp(math.Abs(math.Mod(a, 1)))
			return math.Abs(decision.Not(decision.Not(p))-p) < tol
		}, cfg); err != nil {
			t.Error(err)
		}
	})

	t.Run("de morgan", func(t *testing.T) {
		// Or(a,b) == Not(And(Not(a), Not(b)))
		if err := quick.Check(func(x, y float64) bool {
			a := decision.Clamp(math.Abs(math.Mod(x, 1)))
			b := decision.Clamp(math.Abs(math.Mod(y, 1)))
			left := decision.Or(a, b)
			right := decision.Not(decision.And(decision.Not(a), decision.Not(b)))
			return math.Abs(left-right) < tol
		}, cfg); err != nil {
			t.Error(err)
		}
	})

	t.Run("commutativity", func(t *testing.T) {
		if err := quick.Check(func(x, y float64) bool {
			a := decision.Clamp(math.Abs(math.Mod(x, 1)))
			b := decision.Clamp(math.Abs(math.Mod(y, 1)))
			return math.Abs(decision.And(a, b)-decision.And(b, a)) < tol &&
				math.Abs(decision.Or(a, b)-decision.Or(b, a)) < tol
		}, cfg); err != nil {
			t.Error(err)
		}
	})

	t.Run("results stay in range", func(t *testing.T) {
		if err := quick.Check(func(x, y, z float64) bool {
			a := decision.Clamp(math.Abs(math.Mod(x, 1)))
			b := decision.Clamp(math.Abs(math.Mod(y, 1)))
			c := decision.Clamp(math.Abs(math.Mod(z, 1)))
			for _, v := range []float64{
				decision.And(a, b), decision.Or(a, b), decision.Not(a),
				decision.All(a, b, c), decision.Any(a, b, c),
				decision.AtLeast(2, a, b, c), decision.Exactly(1, a, b, c),
			} {
				if v < 0 || v > 1 || math.IsNaN(v) {
					return false
				}
			}
			return true
		}, cfg); err != nil {
			t.Error(err)
		}
	})

	t.Run("associativity of And and Or", func(t *testing.T) {
		if err := quick.Check(func(x, y, z float64) bool {
			a := decision.Clamp(math.Abs(math.Mod(x, 1)))
			b := decision.Clamp(math.Abs(math.Mod(y, 1)))
			c := decision.Clamp(math.Abs(math.Mod(z, 1)))
			return math.Abs(decision.And(decision.And(a, b), c)-decision.And(a, decision.And(b, c))) < tol &&
				math.Abs(decision.Or(decision.Or(a, b), c)-decision.Or(a, decision.Or(b, c))) < tol
		}, cfg); err != nil {
			t.Error(err)
		}
	})

	t.Run("All agrees with repeated And", func(t *testing.T) {
		if err := quick.Check(func(x, y, z float64) bool {
			a := decision.Clamp(math.Abs(math.Mod(x, 1)))
			b := decision.Clamp(math.Abs(math.Mod(y, 1)))
			c := decision.Clamp(math.Abs(math.Mod(z, 1)))
			return math.Abs(decision.All(a, b, c)-decision.And(decision.And(a, b), c)) < tol
		}, cfg); err != nil {
			t.Error(err)
		}
	})
}

// TestConservativeBoundsDominate: MinAll and MaxAny exist because independence
// is sometimes false. They must actually be the conservative direction.
func TestConservativeBoundsDominate(t *testing.T) {
	rng := rand.New(rand.NewPCG(29, 31))
	for range 5000 {
		ps := []float64{rng.Float64(), rng.Float64(), rng.Float64()}
		if decision.MinAll(ps...) < decision.All(ps...)-tol {
			t.Fatalf("MinAll %v below All for %v", decision.MinAll(ps...), ps)
		}
		if decision.MaxAny(ps...) > decision.Any(ps...)+tol {
			t.Fatalf("MaxAny %v above Any for %v", decision.MaxAny(ps...), ps)
		}
	}

	// The documented example: three restatements of the same question.
	approx(t, decision.All(0.9, 0.9, 0.9), 0.729, "All(0.9 x3)")
	approx(t, decision.MinAll(0.9, 0.9, 0.9), 0.9, "MinAll(0.9 x3)")
}

func TestAtLeastEdges(t *testing.T) {
	// Nothing required is always satisfied.
	approx(t, decision.AtLeast(0, 0.1, 0.2), 1, "AtLeast(0)")
	approx(t, decision.AtLeast(-1), 1, "AtLeast(-1)")
	// More required than exist is impossible.
	approx(t, decision.AtLeast(3, 0.9, 0.9), 0, "AtLeast(3 of 2)")
	// AtLeast(1) is Any; AtLeast(n) over n events is All.
	approx(t, decision.AtLeast(1, 0.5, 0.5), decision.Any(0.5, 0.5), "AtLeast(1) == Any")
	approx(t, decision.AtLeast(2, 0.5, 0.5), decision.All(0.5, 0.5), "AtLeast(n) == All")

	approx(t, decision.Exactly(-1, 0.5), 0, "Exactly(-1)")
	approx(t, decision.Exactly(2, 0.5), 0, "Exactly(2 of 1)")
}

func TestExpected(t *testing.T) {
	approx(t, decision.Expected(0.5, 0.25, 0.25), 1, "Expected")
	approx(t, decision.Expected(), 0, "Expected()")
	// Expectation is linear regardless of correlation, so it does not clamp
	// to 1 the way a probability would.
	approx(t, decision.Expected(1, 1, 1), 3, "Expected(1,1,1)")
}

// TestDeterminism: the same inputs must produce bit-identical outputs, or an
// audit trail is worthless.
func TestDeterminism(t *testing.T) {
	ps := []float64{0.13, 0.87, 0.5, 0.29, 0.61}
	want := []float64{
		decision.All(ps...), decision.Any(ps...),
		decision.AtLeast(3, ps...), decision.Exactly(2, ps...),
	}
	for range 1000 {
		got := []float64{
			decision.All(ps...), decision.Any(ps...),
			decision.AtLeast(3, ps...), decision.Exactly(2, ps...),
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("result %d changed between identical calls: %v vs %v", i, got[i], want[i])
			}
		}
	}
}
