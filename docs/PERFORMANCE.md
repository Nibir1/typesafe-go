# PERFORMANCE.md

Every number here is measured, not estimated. The methodology is at the bottom
so you can reproduce or contradict them.

---

## The headline

**The SDK adds ~35 µs to a call the API answers in 70,000–500,000 µs.**

That is 0.05% of the *fastest* documented response. Turning off the pre-flight
context check halves it, to ~15 µs — worth knowing, almost never worth doing.

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| `SystemOne`, full path, no socket | 41,891 | 12,155 | 177 |
| Same, pre-flight check off | 21,842 | 7,982 | 103 |
| Raw `net/http` + `encoding/json` baseline | 6,552 | 3,591 | 30 |
| **SDK overhead (default)** | **~35,300** | **~8,600** | **~147** |
| **SDK overhead (check off)** | **~15,300** | **~4,400** | **~73** |

The baseline is the same request marshalled, sent through the same transport
and unmarshalled, with no validation, no typed errors, no estimate and no typed
decode. The difference is what this library costs you.

### Where the overhead goes

| Step | ns/op | Runs on every call? |
|---|---|---|
| `EstimateTokens` | 12,570 | Only when the context check or a budget is on |
| Request marshalling | 6,745 | Yes |
| `Validate` | 264 | Yes |
| Decode one Noul answer | 780 | Per answer read |
| Decode one Choice answer | 2,370 | Per answer read |
| Decode one Score answer | 4,876 | Per answer read |
| `Answers.All()` (3 answers) | 11,904 | Only if you call it |

`EstimateTokens` is the single most expensive thing the SDK does per call, and
it exists to refuse a request the API would reject — trading 12 µs against a
wasted round trip and a 422. It is on by default for that reason.

**It used to run unconditionally**, including when nothing read the result,
which made `WithContextLimitCheck(false)` a no-op for cost. These benchmarks
found that; it now runs only when the context check or a `Budget` will consume
it.

---

## Decision composition

All of it runs on your goroutine after the API has answered.

| | ns/op | allocs/op |
|---|---|---|
| `All` / `Any` / `Expected`, 10 terms | 5–8 | 0 |
| `AtLeast(k, …)`, 3 terms | 13 | 0 |
| `AtLeast(k, …)`, 10 terms | 179 | 96 B |
| `AtLeast(k, …)`, 30 terms | 400 | 256 B |
| `AtLeast(k, …)`, 100 terms | 4,941 | 896 B |
| `Policy.Evaluate`, 3 questions | 2,529 | 528 B |
| `Policy.Evaluate`, 10 questions | 13,134 | 1,834 B |
| `Policy.Evaluate`, 30 questions | 34,536 | 4,047 B |

**`AtLeast` is quadratic in the number of terms** — it builds a
Poisson-binomial distribution, and there is no cheaper exact way to do it. At
100 terms it is 5 µs, which is still 0.007% of a 70 ms call, so the quadratic
term does not begin to matter at any question count the API will accept.

`Policy.Evaluate` is dominated by *decoding* the answers it reads, not by the
arithmetic: reading 30 Noul answers out of a response is ~22 µs of the ~35 µs.

---

## The cache

| | ns/op | B/op |
|---|---|---|
| Hit, `WithSharedResponses` | 7,202 | 3,851 |
| Hit, copying (the default) | 10,660 | 4,301 |
| Miss (includes the loopback call) | 162,721 | 20,656 |

A hit replaces a 70–500 ms network call with ~10 µs, so the cache is roughly
**7,000× faster than the call it avoids**. Copying costs ~3 µs and three
allocations; it is the default because handing the same `*SystemOneResponse` to
two goroutines is shared mutable state.

### Key derivation scales with the state

| State size | Hit, ns/op | B/op |
|---|---|---|
| 1 KiB | 19,691 | 11,506 |
| 10 KiB | 102,626 | 89,747 |
| 40 KiB | 345,255 | 372,610 |

The key is a SHA-256 over the canonicalized request, so it is linear in the
size of the state and cannot be avoided — there is no lookup without a key.

**At 40 KiB a cache hit costs ~345 µs**, about 0.5% of a 70 ms call. Still an
enormous win over making the call, but the cache is not free in front of large
states, and a hit rate below a few percent would not pay for itself there.

A 100 KiB state is not in the table because the SDK refuses it: it exceeds the
32,000-token single-question ceiling. The first version of this benchmark
discovered that by failing, which is the context check doing its job.

---

## Batching

`SystemOneBatch` over 100 states at concurrency 16, against a loopback server:
**10.1 ms for the batch, ~101 µs per item**. The per-item figure is lower than
a single call's 141 µs because the items overlap; against the real API, where
each call is 70–500 ms, throughput is bounded by concurrency and the account's
rate limit, not by anything measured here.

---

## What these numbers are not

**They are not API latency.** Nothing here touches the TypeSafe API. The
documented range is 70–500 ms end to end, which is two to three orders of
magnitude larger than every figure above. If your call is slow, it is not this
library.

**They are not a promise.** They are one machine on one day:

```
goos: darwin, goarch: arm64
cpu: Apple M5 Pro
go version go1.27.0
```

Your absolute numbers will differ. The *ratios* — SDK overhead against API
latency, cache hit against cache miss — are what transfers.

---

## Methodology

### Why not `testing.B.Loop`

`B.Loop` arrived in Go 1.24 and this module declares a **1.23 floor**, which is
a published promise. The classic `b.N` loop measures the same thing. The
roadmap called for `B.Loop`; the floor won.

### Why the socket is removed

The first attempt measured a full client call against `httptest` (140.6 µs) and
a raw HTTP baseline against the same server (136.0 µs), and reported the 4.5 µs
difference as SDK overhead.

That number was wrong. Loopback TCP has a standard deviation larger than the
quantity being measured, and the subtraction produced noise — provably, since
`EstimateTokens` alone costs 12.6 µs and runs on that path, which is already
three times the "overhead" the subtraction claimed.

Both benchmarks now run through an in-memory `http.RoundTripper`. Removing the
socket removes the variance, and what is left is the SDK's own work. The
loopback pair is still there, and is the right thing to quote for *end-to-end
with a local server* — just not for isolating overhead.

### Running them

```bash
go test -run=NONE -bench=. -benchtime=1s -count=6 ./...
```

`-count=6` and a look at the spread, rather than a single run. For comparing
two revisions:

```bash
go install golang.org/x/perf/cmd/benchstat@latest
go test -run=NONE -bench=. -count=10 ./... > new.txt
benchstat old.txt new.txt
```

CI runs the benchmarks on every push and fails on a regression beyond the
threshold in `.github/workflows/ci.yml`. A benchmark that only ever runs
locally stops being true within a month.
