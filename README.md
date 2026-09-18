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
.                      the typesafe package — the public API
├── cassette/          record and replay real API traffic
├── typesafetest/      mock, test server, assertions
├── internal/          canonical JSON, state paths, fixture loading
├── tests/
│   ├── contract/      offline: fixtures vs the locked wire contract
│   └── integration/   live API, build-tagged
├── testdata/
│   ├── contract/      golden request/response pairs
│   └── spec/          vendored OpenAPI document
├── docs/
└── scripts/
```

The library lives at the module root so that the import path stays
`github.com/nibir1/typesafe-go` rather than stuttering into `.../typesafe-go/typesafe`.

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

Next: batching, generics, integrations. Full plan in
[docs/Dev_Roadmap.md](docs/Dev_Roadmap.md).

---

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

"TypeSafe", "System One", and "Jev" are names used by TypeSafe AI to identify their API
and models, used here only to describe what this software interoperates with.
