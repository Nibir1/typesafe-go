# typesafe-go

A community-maintained Go SDK for the [TypeSafe](https://typesafe.ai) **System One** API
and its model, **Jev**.

Send one state and a map of typed questions. Get one typed answer per question, with
calibrated probabilities your code can branch on.

> **Status: pre-release, unpublished.** The client, question primitives, typed answers,
> test doubles and resilience are built and verified against the live API. There is no tagged
> release yet — the first will be `v1.0.0`, once the whole SDK is finished. See
> [the roadmap](#roadmap) for what is built and what is not.
>
> Not affiliated with, endorsed by, or sponsored by TypeSafe AI.

---

## Quickstart

```go
package main

import (
    "context"
    "fmt"
    "log"

    typesafe "github.com/nibir1/typesafe-go"
)

func main() {
    client, err := typesafe.NewClient() // reads TYPESAFE_API_KEY
    if err != nil {
        log.Fatal(err)
    }

    resp, err := client.SystemOne(context.Background(), &typesafe.SystemOneRequest{
        State: "Our API has returned 500s for 20 minutes and we cannot process orders.",
        Questions: map[string]typesafe.Question{
            "is_urgent": typesafe.Noul{
                Instructions: "Does this convey urgency?",
            },
            "department": typesafe.Choice{
                Instructions: "Which team should handle this?",
                Criteria: typesafe.Options{
                    "billing":   "Payments, invoicing, refunds",
                    "technical": "Bugs, outages, integrations",
                    "sales":     nil, // interpreted by its name alone
                },
            },
            "severity": typesafe.Score{
                Instructions: "How severe is the incident?",
                Criteria:     typesafe.Levels{"Cosmetic", "Degraded", "Outage"},
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    urgent, _ := resp.Noul("is_urgent")
    team, _ := resp.Choice("department")
    sev, _ := resp.Score("severity")

    level, label := sev.Nearest()
    fmt.Printf("urgent=%.2f  team=%s (%.2f confident)  severity=%d (%v)\n",
        urgent.Noul, team.Choice, team.Confidence, level, label)
}
```

Real output from the live API:

```
urgent=0.96  team=technical (1.00 confident)  severity=2 (Outage)
```

Every question sees the same state and is evaluated in parallel, so asking three costs
barely more than asking one. Pack every question about a given state into a single call.

---

## The three primitives

| | Returns | Use it for |
|---|---|---|
| **`Noul`** | one probability, 0–1 | a yes/no judgment |
| **`Choice`** | the winning option + a distribution + confidence | picking one of a set you define |
| **`Score`** | a weighted position + a distribution + confidence | rating against ordered levels |

```go
typesafe.Noul{Instructions: "Is this spam?"}

typesafe.Choice{
    Instructions: "What is the tone?",
    Criteria:     typesafe.Options{"calm": "Neutral or polite", "angry": "Upset or hostile"},
}

typesafe.Score{
    Instructions: "How urgent is this?",
    Criteria:     typesafe.Levels{"Can wait", "This week", "Today"},
}
```

Answers are always members of the set you supplied. There is no parsing step and no
prose to recover a value from.

### A Noul has no confidence, on purpose

`NoulAnswer` carries a single probability and nothing else. Near 0.5 *is* the model
saying it does not know, so a separate confidence field would be redundant.

This matters because the obvious accessor would hide it:

```go
conf, ok := resp.Confidence("is_urgent")
// ok == false for a Noul — it has none.
```

Returning a bare `0` would read as *maximally uncertain* for a 0.96. Absence is
reported, not substituted.

---

## Errors

Every documented failure has a type, and all of them unwrap to `*APIError`:

```go
resp, err := client.SystemOne(ctx, req)

var rl *typesafe.RateLimitError
if errors.As(err, &rl) {
    time.Sleep(rl.RetryAfter) // 0 when the server sent no preference
}

var ue *typesafe.UnprocessableEntityError
if errors.As(err, &ue) {
    for _, d := range ue.Detail {
        log.Printf("%s: %s", d.Path(), d.Msg)
        // body.questions.frustration.criteria: Field required
    }
}

if errors.Is(err, typesafe.ErrOverloaded) { /* 529, retry later */ }
```

`APIError.RequestID` carries `x-typesafe-request-id`, worth quoting in a support ticket.
Credentials are scrubbed from every error string, including when a server echoes your key
back in its response body.

---

## Retries

On by default, with the same numbers as the official Python and JavaScript SDKs — two
retries, 500ms backoff doubling to a 5s ceiling, ±25% jitter, within a 30s overall
budget. Porting a working integration should not require re-tuning anything.

```go
client, _ := typesafe.NewClient(
    typesafe.WithMaxRetries(5),                  // adjust one field
    typesafe.WithRetryPolicy(typesafe.NoRetry()), // or turn it off
)
```

`408`, `429`, `5xx` and `529` are retried. **`422` never is** — a request that failed
validation will fail identically, and retrying only delays the error.

Three behaviors worth knowing:

- **Retried requests send byte-identical bytes.** The body is marshalled once. Re-doing
  it per attempt would risk a different request, since Go iterates maps randomly.
- **A `retry-after` longer than 30s is not waited out.** The error is returned at once
  with the server's requested delay still on it, rather than parking your caller on the
  server's say-so. Configurable via `MaxRetryAfter`.
- **Backoff never outlasts the budget.** If the next delay would exceed the remaining
  time, the client stops instead of sleeping toward a deadline it will miss.

`ErrRetriesExhausted` wraps the terminal failure rather than replacing it, so
`errors.As` still finds the `*RateLimitError` underneath and existing handling keeps
working.

### Circuit breaker

Off by default. Retrying helps with a blip and hurts during an outage.

```go
breaker := typesafe.NewCircuitBreaker()          // 5 failures, 30s open
client, _ := typesafe.NewClient(typesafe.WithCircuitBreaker(breaker))
```

Only retryable failures count toward opening it — a `422` says your request was wrong,
not that the service is unwell. Share one breaker per upstream.

---

## Testing without a key

`go test ./...` in your project should pass on a laptop with no network and no
credentials. Three doubles, by how much of the client they exercise:

```go
// Unit test: your branching logic, transport incidental.
m := typesafetest.NewMock().On("is_spam", typesafetest.Noul(0.93))

// Integration test: real client, scripted server.
srv := typesafetest.NewServer(t, typesafetest.RateLimited(3), typesafetest.Answers(...))

// Against real recorded answers, replayed offline forever.
hc := cassette.Open(t, "testdata/cassettes/triage.jsonl", cassette.Recording(*update))
```

Cassettes replay through an `http.RoundTripper`, so the SDK's marshalling, status
mapping, and error typing all still run. They are newline-delimited JSON, diffable, and
byte-deterministic — re-record one and an empty diff means the API has not drifted.

Full guide: **[docs/TESTING.md](docs/TESTING.md)**.

---

## Deciding, not just asking

TypeSafe's documentation is consistent: decompose a broad judgment into atomic
questions, ask them together, and combine the answers with deterministic logic in code.
Every SDK hands back a probability distribution and stops there. The `decision` package
is the part that was left as an exercise.

```go
var SpamPolicy = decision.Policy{
    Name: "spam-v3",
    Weights: decision.Weights{
        "asks_for_credentials":  0.4,
        "creates_time_pressure": 0.3,
        "has_suspicious_link":   0.2,
        "generic_greeting":      0.1,
    },
    Normalize:   true,
    ReviewAbove: 0.5,
    BlockAbove:  0.8,
}

result, err := SpamPolicy.Evaluate(resp)
switch result.Verdict {
case decision.Block:  quarantine()
case decision.Review: queueForHuman()
}
```

A `Policy` is **data**, so it round-trips through JSON: thresholds can live in config,
be diffed in review, and reload without a deploy. `Validate()` catches a `ReviewAbove`
above `BlockAbove`, which makes a verdict unreachable with nothing at runtime to say so.

`result.Trace` shows which question contributed what, ordered by contribution. A
probability in an audit log without its derivation is not evidence of anything.

### Probability, done as probability

```go
decision.Or(0.9, 0.8)             // 0.98 — a + b - ab, not a + b
decision.AtLeast(2, 0.5, 0.5, 0.5) // 0.5  — exact Poisson-binomial
decision.MinAll(0.9, 0.9, 0.9)     // 0.9  — no independence assumed
```

`AtLeast` answers a question a weighted sum structurally cannot: *how likely is it that
**several** of these warning signs are real?* A sum of 1.5 cannot distinguish three
half-certain signals from one certain and one absent.

`MinAll`/`MaxAny` exist because independence is sometimes false — ask "is this urgent?"
and "is this time-sensitive?" about the same state and multiplying understates badly.

### Confidence as a second axis

```go
routing := decision.Bands{ActAbove: 0.9, ConfirmAbove: 0.5}
switch routing.Classify(department.Confidence) {
case decision.Act:      route(department.Choice)
case decision.Confirm:  askUser(department.Choice)
case decision.Escalate: routeToHuman()
}
```

`ClassifyAnswer` on a **Noul** returns an error rather than a band — a Noul has no
confidence, and banding one would read a zero and escalate every call.

### Weights from data, not guesswork

```go
report, err := decision.Calibrate(labeledHistory, questionIDs, decision.CalibrationOptions{})
if !report.Separable {
    return fmt.Errorf("these signals do not predict the label:\n%s", report)
}
policy := decision.NewPolicy("spam-v4", report.Weights)
```

Logistic regression, stdlib only. **Check `Separable`** — a fit on noise still returns
weights, and those weights still produce confident-looking verdicts. The bar is the
majority-class rate plus two standard errors, so it scales with how much data you have.

Nothing in `decision` touches the network. Given the same answers it returns the same
verdict, which is what makes a decision replayable months later — enforced by an
architecture test.

---

## Catching mistakes before they cost you

Two checks with no equivalent in any other TypeSafe client.

**Unresolvable state references.** TypeSafe's docs recommend pointing a question at part
of a structured state by backticked path. The server does not resolve these — the model
just sees a path naming nothing and answers anyway. Nothing in the response tells you.

```go
for _, w := range req.CheckReferences() {
    log.Warn(w) // question "q" references `ticket.mesage`, which does not
                // resolve: `ticket` has no key "mesage"
}
```

**Requests that would exceed the context window**, caught before the round trip:

```go
est := req.EstimateTokens()
if err := est.Err(); err != nil {
    return err // never sent
}
```

The API enforces **two** ceilings — 64k for the whole request and 32k for the state plus
any *single* question — and a request can pass the first and fail the second. The
server's error does not say which. The estimator is fitted from live measurement
(`tokens ≈ 239 + 0.331 × wire_bytes`, plus ~240 fixed per call) and deliberately
over-reports by ~25%, because an estimate that is sometimes *under* fails exactly when a
request is near a limit.

`typesafe.Budget` caps requests and tokens per window and refuses before any network
I/O, so a runaway loop costs nothing.

**Requests the server would reject**, caught before the round trip:

```go
warnings, err := req.Validate()
// err      — a Score with 11 levels, a Choice with no options, a missing state
// warnings — legal, but probably not what you meant
```

The Score ceiling is a good example of why this exists: the API caps a rubric at **10
levels** and returns `400` above that. Neither the OpenAPI schema nor the prose
documentation mentions the limit. We found it by sending eleven.

---

## Composing it into an application

Interceptors wrap one *logical* call — retries happen beneath, so a latency histogram
records what the caller waited for rather than one bar per attempt:

```go
client, _ := typesafe.NewClient(
    typesafe.WithInterceptor(tracing, metrics, typesafe.WithLogging(slog.Default())),
    typesafe.WithRequestID(uuid.NewString),
    typesafe.WithHooks(typesafe.Hooks{
        OnResponse: func(ctx context.Context, i typesafe.CallInfo) {
            log.Printf("%s: %d tokens in %s", i.RequestID, i.InputTokens, i.Duration)
        },
    }),
)
```

They compose outermost-first. A panic in an interceptor or hook becomes a
`*PanicError` with the stack intact, rather than taking down the request path.

`WithLogging` logs the request id, question count, duration, model and usage — and
never the state, which is your data and routinely holds personal information.

### Fan-out

```go
results := client.SystemOneAll(ctx, reqA, reqB, reqC) // positional, one error each
r := <-client.SystemOneAsync(ctx, req)
```

Every question about *one* state belongs in a single request — they run in parallel
server-side. This is for fanning out over different states.

### Batching thousands of states

```go
result := client.SystemOneBatch(ctx, states, typesafe.Questions{
    "is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
    "team":      typesafe.NewChoice("Which team?").Options("billing", "technical"),
}, typesafe.WithConcurrency(16))

if err := result.Err(); err != nil {
    log.Print(err) // "3 of 1000 batch items failed \n 2 x ... \n 1 x ..."
}
for i, item := range result.Items { // input order, always
    if item.Err != nil {
        continue // one failure never aborts the batch
    }
    use(i, item.Response)
}
```

A bounded worker pool with **per-item error isolation**, results **in input order**,
and a summed `Usage`. `SystemOneAll` is the unbounded form for a handful of requests;
this is the one for thousands.

Concurrency adapts **globally**: when any worker meets a `429` or `529` the limit for
the whole batch halves, and it climbs back one at a time after a run of successes —
never above the `WithConcurrency` ceiling you set. Without a shared limit, every worker
independently rediscovers the same rate limit while the batch keeps pushing at the rate
that caused it. `WithAdaptiveConcurrency(false)` pins it.

It batches **states, never questions**. Jev ingests the state once and evaluates every
question against it in parallel, so a second question costs only its own tokens while a
second state costs a whole request.

For a batch too large to hold in memory, or when downstream work can start on the first
result, range over the stream instead — results arrive in *completion* order, and
`ItemResult.Index` gives the input position:

```go
for i, item := range client.SystemOneBatchSeq(ctx, states, qs) {
    ...
    if enough { break } // cancels the batch; no goroutine outlives the loop
}
```

Breaking out is the only thing that drops a pending result — nobody is waiting for it.
A cancelled *context* still yields one item per input, each carrying the context error,
so a consumer can tell "cancelled after three" from "cancelled before anything started".

### Compile-time typed questions

Declare the option set once, as a Go enum, and the compiler checks both ends:

```go
type Topic string

const (
    TopicBilling   Topic = "billing"
    TopicTechnical Topic = "technical"
    TopicOther     Topic = "other"
)

q := typesafe.TypedChoice[Topic]("Which team should handle this?",
    typesafe.OptionOf(TopicBilling, "Invoices, charges, refunds"),
    typesafe.OptionOf(TopicTechnical, "Bugs, outages, API errors"),
    typesafe.OptionOf(TopicOther, nil), // null description, read by name alone
)

ans, err := q.Answer(resp, "department")
switch ans.Choice {          // Topic, not string
case TopicBilling:   ...
case TopicTechnical: ...
}
ans.Probabilities[TopicBilling] // map[Topic]float64
```

`TypedScore` does the same for a rubric, where a level's position **is** its score:

```go
type Frustration int
const (
    Calm Frustration = iota
    Annoyed
    Angry
)

q := typesafe.TypedScore[Frustration]("How frustrated is the customer?",
    typesafe.LevelOf(Calm, "No sign of irritation"),
    typesafe.LevelOf(Annoyed, "Clearly unhappy, still civil"),
    typesafe.LevelOf(Angry, "Hostile, threatening to leave"),
)

if ans.AtOrAbove(Angry) > 0.8 { escalate() }
```

A rubric declared out of order would map every answer to the wrong label with nothing
in the score to reveal it, so `Validate` rejects one whose values do not match their
positions.

These are wrappers, not a parallel implementation: each embeds the plain question and
marshals through the same code, so the JSON is byte-identical and `Untyped()` converts
an answer back losslessly.

`Exhaustive` catches the one thing types cannot — an answer naming an option the
question never declared, which means the request and the enum have drifted apart:

```go
if err := typesafe.Exhaustive(ans, AllTopics...); err != nil { ... }
```

The question's own `Answer` method does this for you, against the set it declared.

### Generating questions from enums

```bash
go install github.com/nibir1/typesafe-go/cmd/typesafe-gen@latest
```

```go
//go:generate typesafe-gen -type TicketQuestions

// TicketQuestions is everything asked about one support ticket.
type TicketQuestions struct {
    // Which team should handle this ticket?
    Department Topic `typesafe:"choice"`

    // How severe is the problem described here?
    Severity Severity `typesafe:"score,id=severity"`
}
```

Generates `Questions()`, a typed constructor and a checked accessor per field, and an
`AllTopic` slice — descriptions taken from each constant's doc comment. The option set
otherwise exists twice, as the enum the code switches on and as the criteria map the
request carries, and nothing reports it when they drift.

### Fluent constructors

```go
typesafe.NewChoice("Which team should handle this?").
    Option("billing", "Payments, invoicing, refunds").
    Option("technical", "Bugs, outages, integrations")
```

Structs remain the documented default; both forms marshal byte-identically and get the
same validation.

---

## Static analysis

Three `go/analysis` analyzers, in a separate module so the core keeps its zero
dependencies:

```bash
go install github.com/nibir1/typesafe-go/lint/cmd/typesafe-lint@latest
go vet -vettool=$(which typesafe-lint) ./...
```

| | Catches |
|---|---|
| `atomicquestion` | Compound questions — one probability covering two propositions, which no threshold can split apart |
| `jaggededge` | Questions hitting a **documented** Jev failure mode: counting, date comparison, hex values, double negatives, generation, inverted Noul criteria |
| `confidencecheck` | Branching on an answer without reading its confidence; bare threshold literals; discarding the `ok` from `Confidence()` |

Every `jaggededge` rule cites a section of TypeSafe's published model-jaggedness notes
and repeats its recommended fix. That makes it a conformance checker rather than an
opinion:

```
Noul instructions contain "how many". Jev does not count reliably — it recognizes
the shape of an answer rather than tallying, and the error grows with the size of
the thing being counted. Ask one Noul per item and sum the answers in code
(jaggedness: Math and Numbers: Counting)
```

`//nolint:jaggededge <reason>` suppresses a finding, and a suppression **without** a
reason is itself reported.

---

## Configuration

Resolution order, matching the official Python and JavaScript SDKs, so a process already
configured for either works here unchanged:

| Setting | Order | Default |
|---|---|---|
| API key | `WithAPIKey` → `TYPESAFE_API_KEY` → error | — |
| Base URL | `WithBaseURL` → `TYPESAFE_BASE_URL` → default | `https://api.typesafe.ai` |
| Model | per-request → `WithDefaultModel` → `TYPESAFE_DEFAULT_MODEL` → default | `jev-latest` |
| Timeout | `WithTimeout` → default | `10s` per operation |

```go
client, err := typesafe.NewClient(
    typesafe.WithDefaultModel("jev-1.13.0"), // pin a version; aliases move
    typesafe.WithTimeout(5*time.Second),
    typesafe.WithLogger(slog.Default()),
)
```

The logger never receives your API key or your request state.

---

## Command line

```bash
go install github.com/nibir1/typesafe-go/cmd/typesafe@latest
```

```bash
# Ask three questions about a ticket, no file needed
typesafe run --state-text "Payouts have failed for 3 days" \
  --noul urgent="Does this convey urgency?" \
  --choice team="billing,technical,sales" \
  --score severity="Low,Medium,High"

typesafe estimate -f request.json   # tokens and cost, sends nothing
typesafe lint -f request.json       # problems, before you pay for them
typesafe doctor                     # why is my setup not working?
typesafe explain --policy p.json --answer is_spam=0.93
```

Also `models`, `record`, `replay`, `completion`, and `version`. Everything except
`run`/`models`/`doctor`/`record` works offline with no key.

`doctor` returns a distinct exit code per cause — 3 credential, 4 network, 7 unknown
model, 6 rate limited — so a script can branch without parsing stderr.

The binary has **no dependencies either**: stdlib `flag`, no CLI framework.

---

## Requirements

**Go 1.23+.** The core module has **zero third-party dependencies**, and CI fails if that
ever changes.

---

## Repository layout

```
.                      the typesafe package — client, primitives, answers,
                       retries, budget, middleware, batching
├── decision/          ★ composition: algebra, policies, bands, calibration
├── cassette/          record and replay real API traffic
├── typesafetest/      mock, test server, assertions
├── cmd/typesafe/      the CLI — same module, so still zero dependencies
├── cmd/typesafe-gen/  ★ go:generate questions from Go enums
├── lint/              ★ the analyzers — a SEPARATE module (needs x/tools)
│   ├── atomicquestion/  jaggededge/  confidencecheck/
│   └── cmd/typesafe-lint/
├── internal/          canonical JSON, state paths, token estimator, fixtures
├── tests/
│   ├── contract/      offline: fixtures vs the locked wire contract
│   ├── typecheck/     negative-compilation tests for the typed API
│   └── integration/   live API, build-tagged
├── testdata/
│   ├── contract/      golden request/response pairs
│   └── spec/          vendored OpenAPI document
├── docs/  scripts/  Makefile
```

**The library lives at the module root** so the import path stays
`github.com/nibir1/typesafe-go` rather than stuttering into `.../typesafe-go/typesafe`.

**`lint/` is a separate module** because `go/analysis` comes from `golang.org/x/tools`.
That split is what keeps the core's zero-dependency guarantee true — importing the SDK
pulls in nothing.

---

## Development

```bash
make            # list every target
make verify     # the full offline gate — run before pushing
make live       # the above, plus the live API (needs a key)
```

`make verify` runs: tidy check, gofmt, `go vet` under both build tags, the
zero-dependency assertion, race tests, the contract suite, fixture validation against the
OpenAPI schema, a credential scan over every committed fixture, and a doc-coverage check.

To enable the live targets, put your key where git will not see it:

```bash
printf 'TYPESAFE_API_KEY=%s\n' "$YOUR_KEY" > .env.local && chmod 600 .env.local
```

`.env.local` is gitignored, and `make verify` fails if it ever becomes tracked.

---

## Documentation

| | |
|---|---|
| [docs/WIRE_CONTRACT.md](docs/WIRE_CONTRACT.md) | The verified wire contract, and every place the published docs are wrong |
| [docs/TESTING.md](docs/TESTING.md) | Testing without a key |
| [docs/LINTING.md](docs/LINTING.md) | The three analyzers, their rules, and CI wiring |
| [docs/Dev_Roadmap.md](docs/Dev_Roadmap.md) | Phase plan, competitive audit, design corrections |
| [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) | Attribution register |

`WIRE_CONTRACT.md` is worth reading before building anything non-trivial. Several things
the official documentation states are contradicted by the live API, including the Score
minimum, the Score maximum, the `400` status, the shape of `detail`, and the format of
`release_date`.

---

## Roadmap

Built and verified:

- **Phase 0** — wire contract locked, OpenAPI vendored, 10 golden fixtures, drift detection
- **Phase 1** — client, options, typed errors, `/v1/models`
- **Phase 2** — `Noul`/`Choice`/`Score`, typed answers, validation, reference checking
- **Phase 3** — cassettes, mock, test server, canonical hashing
- **Phase 4** — retries, backoff, retry budget, circuit breaker
- **Phase 6** — **the `decision` package**: probability algebra, weighted policies,
  confidence bands, routing, and weight calibration from labeled data
- **Phase 7** — context-budget and cost pre-flight, quota guard
- **Phase 8** — the `typesafe` CLI
- **Phase 9** — **three static analyzers**: `atomicquestion`, `jaggededge`,
  `confidencecheck`
- **Phase 10** — interceptors, hooks, async fan-out, fluent constructors
- **Phase 11** — **batching**: bounded worker pool, per-item error isolation, input
  ordering, globally adaptive concurrency, and an `iter.Seq2` streaming view
- **Phase 12** — **compile-time typed questions**: `TypedChoice`/`TypedScore` over your
  own enums, `Exhaustive` drift detection, and `typesafe-gen`

Next: observability, integrations, release engineering. Full plan in
[docs/Dev_Roadmap.md](docs/Dev_Roadmap.md).

---

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

"TypeSafe", "System One", and "Jev" are names used by TypeSafe AI to identify their API
and models, used here only to describe what this software interoperates with.
