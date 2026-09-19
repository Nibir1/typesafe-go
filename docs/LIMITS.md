# LIMITS.md

Every ceiling you can hit, what happens when you do, and what the SDK does
about it.

---

## The numbers

| Limit | Value | Enforced by |
|---|---|---|
| Whole-request context | 64,000 tokens | SDK, before sending |
| Single question + state | 32,000 tokens | SDK, before sending |
| `Score` levels | 1–10 | SDK, before sending |
| `Choice` options | ≥ 1 | SDK, before sending |
| Requests per minute | 1,200 | Server (429) |
| Tokens per second | 250,000 | Server (429) |

The first four fail locally with `ErrInvalidRequest` and cost no round trip.
The last two come back as `*RateLimitError`, which the retry policy honours.

---

## The two context ceilings

There are **two**, and hitting the second while well under the first is the
surprise:

```
state + every question combined   <= 64,000
state + the single longest question <= 32,000
```

A large state with several questions can be inside the whole-request limit and
still fail, because each question is measured *with the whole state*. The state
is counted once for the total and once again per question.

```go
est := req.EstimateTokens()
fmt.Println(est.Total, "/", typesafe.MaxContextTokens)
fmt.Println(est.LongestSingle, "/", typesafe.MaxSingleQuestionTokens, "for", est.LongestQuestionID)

if err := est.Err(); err != nil {
    // names which ceiling, and which question
}
```

`typesafe estimate -f request.json` prints the same breakdown from the command
line.

### How the estimate works

It is a **measured** model, not a tokenizer: a fixed per-request overhead plus a
linear function of compact JSON bytes.

```
tokens ≈ 240 + 0.413 × bytes
```

The slope was measured against the live API and then given 25% headroom, so the
estimate **over-reports by design**. An estimate that occasionally under-reports
would let a request through that the API then rejects, which is the failure the
check exists to prevent.

Two things it got wrong during development, both fixed and both worth knowing
about if you write your own:

- Summing the parts and forgetting the request envelope.
- Measuring *pretty-printed* bytes when the wire carries compact JSON — 35%
  larger, making the slope 53% too shallow.

---

## Rate limits

The published limits are 1,200 requests per minute and 250,000 tokens per
second. They are account-wide, so everything else you run shares them.

### What the SDK does

**Retries with backoff**, honouring `Retry-After` when the server sends it:

```go
typesafe.WithRetryPolicy(typesafe.RetryPolicy{
    MaxRetries:        2,
    BackoffInitial:    500 * time.Millisecond,
    BackoffMax:        5 * time.Second,
    BackoffJitter:     0.25,
    RespectRetryAfter: true,
    MaxRetryAfter:     30 * time.Second,
})
```

`MaxRetryAfter` matters: a server asking you to wait five minutes is not a
reason to block the caller for five minutes. Past the cap, the typed error is
returned with `RetryAfter` on it so you can decide.

**Adapts batch concurrency globally.** When any worker in a `SystemOneBatch`
meets a 429, the limit for the *whole batch* halves and then recovers one step
at a time. Without a shared limit, N workers each rediscover the same ceiling
while the batch keeps pushing at the rate that caused it.

**Refuses locally, with a `Budget`:**

```go
typesafe.WithBudget(typesafe.NewBudget(
    typesafe.MaxRequestsPerMinute(600),      // half the published limit
    typesafe.MaxTokensPerSecond(125_000),
    typesafe.MaxTotalTokens(5_000_000),      // a hard spend cap
))
```

A `Budget` fails fast with `ErrBudgetExceeded` instead of making a request that
will be rejected. **It counts in one process** — with several replicas each
enforces the cap independently and the account sees the sum. Size it per
replica, or enforce the real limit somewhere cluster-wide.

**Opens a circuit** when failures persist, so a dependency that is down stops
being asked:

```go
typesafe.WithCircuitBreaker(&typesafe.CircuitBreaker{
    Threshold: 5, OpenFor: 30 * time.Second, HalfOpenProbes: 1,
})
```

---

## Model jaggedness

The hardest limits are not numeric. TypeSafe publishes a list of things Jev does
poorly, and none of them produce an error — you get a confident, plausible,
wrong answer.

| Weak at | Why | Do this instead |
|---|---|---|
| Counting | Recognizes the shape of an answer rather than tallying; error grows with the count | One `Noul` per item, sum in code |
| Date comparison | Dates are read as text, not ordered quantities | Parse and compare in code |
| Arithmetic | Unreliable | Extract the value, compute in code |
| Encodings (hex, RGB, base64) | Read as text | Decode in code first |
| Double negatives | Degrade measurably | Rewrite as a presence |
| Generation | Not what System One is for | Use a generative model |
| Sentinels (`"*"`, `-1`, `"all"`) | A convention your code knows and the model does not | Validate with an `if` |

**Nothing in the response reveals any of this.** There is no field that says
"that question was unsuitable". Build time is the only place to catch it, which
is what the [analyzers](LINTING.md) are for:

```bash
go vet -vettool=$(which typesafe-lint) ./...
```

Every `jaggededge` rule cites the section of TypeSafe's notes it comes from, so
it is a conformance checker rather than an opinion.

---

## Cost

**Only input tokens are billed.** Output tokens are reported and free, so a cost
panel that sums both overstates spend.

The state is the expensive part, and it is sent in full on every call. That
single fact drives three of this SDK's design decisions:

- **Pack questions into one call.** A second question costs its own tokens; a
  second *state* costs a whole request. See
  [`speculative_fanout`](../examples/speculative_fanout).
- **`SystemOneBatch` batches states, never questions.**
- **[`typesafecache`](../typesafecache) exists at all.** Re-evaluating the same
  content is the largest avoidable cost in a typical integration.

```bash
typesafe estimate -f request.json    # tokens and an approximate price
```

---

## What has no limit, and should

**Nothing bounds how many questions you put in one request** except the context
ceiling. Two hundred questions about one state is legal, will be accepted, and
will produce two hundred answers of declining usefulness — each one is still a
separate judgement, and a question set nobody can review is a question set
nobody is reviewing.

**Nothing bounds how long you wait overall** unless you set it. `WithTimeout`
covers one attempt; `RetryPolicy.Timeout` covers the whole call including
backoff. Set both, or a retried call can outlive the request that started it.

**Nothing stops you caching for a week.** `typesafecache` will do it. The TTL is
also the window in which a moved model alias goes unnoticed, so a long TTL
against `jev-latest` is a deliberate trade — see
[OBSERVABILITY.md](OBSERVABILITY.md).

---

See the [user manual](MANUAL.md) for how these limits shape everyday use, and
[PERFORMANCE.md](PERFORMANCE.md) for what the SDK itself costs.
