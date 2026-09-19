# TESTING.md

How to test code that calls TypeSafe, without needing a live API key.

That is the whole goal of this SDK's testing story: `go test ./...` in your project
should pass on a laptop with no network and no credentials, and still exercise the real
client — its marshalling, its status mapping, its typed errors.

---

## Pick a double

Three options, testing different things. Pick the cheapest one that still exercises what
you care about.

| | What it is | Use it when | Exercises the client? |
|---|---|---|---|
| **`typesafetest.Mock`** | In-memory `typesafe.API` | Your code branches on answers and the transport is incidental | No |
| **`typesafetest.Server`** | Real `httptest` server | You care about errors, retries, or what was sent on the wire | Yes |
| **`cassette`** | Recorded real traffic, replayed | You want the model's actual answers, reproducibly | Yes |

A mock is fastest to write and tests the least. A cassette is the most faithful and
needs one live call to create. Most projects want a mock for unit tests and a handful of
cassettes for the paths that matter.

---

## Mock

```go
func TestTriage(t *testing.T) {
    m := typesafetest.NewMock().
        On("is_urgent", typesafetest.Noul(0.94)).
        On("department", typesafetest.Choice(map[string]float64{
            "billing": 0.08, "technical": 0.89, "sales": 0.03,
        }))

    queue, err := triage(context.Background(), m, ticket)
    if err != nil {
        t.Fatal(err)
    }
    if queue != "queue-eng" {
        t.Errorf("queue = %q", queue)
    }
}
```

Write your own code against `typesafe.API` rather than `*typesafe.Client`, and the mock
drops in:

```go
func triage(ctx context.Context, api typesafe.API, tk Ticket) (Queue, error)
```

Two behaviors are worth knowing about:

**It validates like the real client.** A Score with eleven levels, a Choice with no
options, a missing state — all rejected exactly as the API would reject them. A mock
that accepted those would let a test pass against a request that could never work.

**An unprogrammed question is an error, not a zero.** Returning a zero-valued answer
would read as a genuine low-confidence result, and you would spend an afternoon on it.
The error names the question and the call that fixes it.

Queue a failure to test an error path:

```go
m.ReturnError(&typesafe.RateLimitError{ /* ... */ })  // first call only
m.On("q", typesafetest.Noul(0.8))                     // every call after
```

---

## Server

When the client's own behavior is the thing under test:

```go
func TestHandlesRateLimit(t *testing.T) {
    srv := typesafetest.NewServer(t,
        typesafetest.RateLimited(3),                            // first call
        typesafetest.Answers(map[string]any{                    // then succeed
            "q": typesafetest.Noul(0.8),
        }),
    )

    client, err := typesafe.NewClient(
        typesafe.WithAPIKey("test"),
        typesafe.WithBaseURL(srv.URL),
    )
    // ...
}
```

Responses are served in order, and the last one repeats, so a test that does not care
how many calls happen does not have to count them.

> **Turn retries off when asserting single-attempt behavior.** Retries are on by
> default, so one `SystemOne` call will consume several scripted `429` or `5xx`
> responses and spend real time in backoff. Tests that assert which status maps to
> which error want:
>
> ```go
> typesafe.WithRetryPolicy(typesafe.NoRetry())
> ```
>
> This repository's own suites learned that the noisy way: adding retries turned a
> 0.3s package into a 27s one and broke a test that had scripted three failures.

### Every documented failure

| Helper | Produces |
|---|---|
| `BadRequest(msg)` | `400`, object detail shape |
| `Unauthorized()` | `401`, the live message |
| `Unprocessable(loc, msg, kind)` | `422`, array detail shape with a field path |
| `RateLimited(seconds)` | `429`, with `retry-after`; pass `0` to omit it |
| `Overloaded()` | `529` |
| `Status(code)` | any bare status |
| `Malformed(body)` | `200` with a body that is not the documented shape |
| `Answers(map)` | `200` with answers |
| `Models(names...)` | `GET /v1/models` |
| `FailingTransport(err)` | a transport failure, no socket opened |

Answer builders derive their dependent values, so a fixture is always internally
consistent: `Choice` computes the winner and confidence from the distribution, and
`Score` computes the probability-weighted mean the same way the API does. You cannot
accidentally write a fixture whose numbers disagree.

### Asserting what was sent

```go
last, _ := srv.LastRequest()
body, _ := last.Request()
if body["model"] != "jev-latest" { ... }
```

---

## Cassettes

A cassette records real traffic once and replays it offline forever.

```go
var update = flag.Bool("update", false, "re-record cassettes")

func TestTriageAgainstRealAnswers(t *testing.T) {
    hc := cassette.Open(t, "testdata/cassettes/triage.jsonl", cassette.Recording(*update))

    client, err := typesafe.NewClient(
        typesafe.WithAPIKey(keyOr("replay")), // any non-empty value when replaying
        typesafe.WithHTTPClient(hc),
    )
    if err != nil {
        t.Fatal(err)
    }

    resp, err := client.SystemOne(context.Background(), req)
    // ... assert on real answers

    cassette.AssertAllPlayed(t, hc)
}
```

Record once:

```bash
TYPESAFE_API_KEY=sk-... go test -run TestTriageAgainstRealAnswers -update ./...
```

Then commit the cassette. Every run after that is offline.

### Why it replays through the transport

The cassette is an `http.RoundTripper`, not a stubbed client method. On replay the SDK
still marshals the request, maps the status, builds the typed error, and validates the
response. A double that replaced `SystemOne` would skip all of that, and a test written
against it would not notice if any of it broke.

### Matching

The key is a SHA-256 over the method, path, and **canonical** request body. Canonical
means object keys sorted and whitespace stripped, so a request re-marshalled in a
different order — which Go does on every call, since map iteration is randomized — still
finds its recording.

Array order is preserved, because a Score rubric's order is its meaning. Reorder the
levels and you get a different key, correctly.

A miss reports the request, the computed key, how many interactions the cassette holds,
and how to re-record.

### Determinism

Cassettes contain no timestamps, no durations, and no request ids. Recording the same
interactions twice produces byte-identical files on any machine.

That is what makes re-recording a drift check: refresh a cassette, and if the diff is
empty the API has not changed. If it is not empty, the diff shows exactly which field
moved.

### Secrets

Scrubbing happens at **write** time, so a credential cannot reach the file even if the
process dies afterwards.

- The `Authorization` header is never recorded.
- The recorder's own API key is replaced with `[REDACTED]` in bodies and headers.
- Register anything else with `cassette.WithSecret("...")`.

> **Note on 422 bodies.** The API echoes your request back in the `input` field of a
> validation error. If your state contains personal data it will be in that response,
> and therefore in a cassette that records one. Register it as a secret, or use a
> synthetic state for fixtures.

Verify a committed cassette independently:

```go
if err := cassette.Verify("testdata/cassettes/triage.jsonl", apiKey); err != nil {
    t.Error(err)
}
```

`Verify` also fails on any `Bearer` or `authorization` marker, in case a future change
starts recording headers.

---

## Testing a decision policy

A `decision.Policy` is pure arithmetic over numbers, so it needs no client, no mock and
no network. `decision.Nouls` is a bare map that satisfies the same `Source` interface a
real response does:

```go
func TestSpamPolicy(t *testing.T) {
    cases := []struct {
        name    string
        answers decision.Nouls
        want    decision.Verdict
    }{
        {"clean", decision.Nouls{"asks_for_credentials": 0.01, "generic_greeting": 0.05}, decision.Allow},
        {"obvious", decision.Nouls{"asks_for_credentials": 0.98, "generic_greeting": 0.9}, decision.Block},
    }
    for _, tc := range cases {
        got, err := SpamPolicy.Evaluate(tc.answers)
        if err != nil { t.Fatal(err) }
        if got.Verdict != tc.want {
            t.Errorf("%s: %s (score %.4f)\n%s", tc.name, got.Verdict, got.Score, got.Trace)
        }
    }
}
```

Two things worth building into such a test:

**Validate the policy at startup, and in a test.** `policy.Validate()` catches a
`ReviewAbove` above `BlockAbove`, which makes a verdict unreachable with nothing at
runtime to tell you.

**Print the trace on failure.** `Result.Trace` shows which question contributed what,
ordered by contribution. A verdict without its derivation tells you nothing about why
the threshold was wrong.

Traces are deterministic across runs, so they can be diffed and snapshotted.

---

## Testing retry behavior

Waiting out real backoff makes a suite slow without proving the delays were right. Two
approaches, both used here:

**Inject a clock** (works on every supported Go version). This SDK's own retry tests use
an unexported `withClock` option; your code can do the same by wrapping the transport and
asserting on timing indirectly, or by scripting responses and counting calls.

**`testing/synctest`** (Go 1.25+). Inside a bubble, time is virtual and only advances
once every goroutine is durably blocked, so a 30-second retry budget elapses instantly
while the production timer code runs unmodified.

> One constraint worth knowing: **synctest does not work with a real network.** A
> goroutine blocked on a socket read is not *durably* blocked, so the clock never
> advances and the test hangs until the binary times out. Use an in-memory
> `http.RoundTripper` inside a bubble, not `httptest.NewServer`.

## Testing interceptors and async calls

An interceptor wraps one logical call, so a test asserting order does not need a server
that fails:

```go
c, _ := typesafe.NewClient(
    typesafe.WithAPIKey("test"),
    typesafe.WithBaseURL(srv.URL),
    typesafe.WithInterceptor(recordOrder("a"), recordOrder("b")),
)
// a runs outermost: a:in, b:in, b:out, a:out
```

Retries happen *beneath* the innermost handler. If you are asserting per-attempt
behavior, use `WithRetryObserver`, not an interceptor.

A panic in an interceptor or hook becomes a `*typesafe.PanicError` rather than crashing
the test binary, and the captured stack names the panicking frame:

```go
var pe *typesafe.PanicError
if errors.As(err, &pe) {
    t.Logf("panicked with %v at:\n%s", pe.Value, pe.Stack)
}
```

`SystemOneAsync` returns a buffered channel that always closes after its one result, so
a test can safely abandon a call:

```go
r := <-client.SystemOneAsync(ctx, req)   // one result, then closed
```

A cancelled call still delivers its error on the channel — it does not close early — so
a `select` waiting on it is always woken exactly once.

---

## Testing a batch

`SystemOneBatch` fails each item independently, so a test asserts on the slice rather
than on one error. Results are always in input order, including the ones that failed
and the ones a cancellation stopped from ever starting:

```go
result := client.SystemOneBatch(ctx, states, qs, typesafe.WithConcurrency(8))

for i, item := range result.Items {   // len == len(states), always
    switch {
    case item.Err != nil:
        t.Logf("state %d failed: %v", i, item.Err)
    default:
        assertAnswer(t, item.Response)
    }
}
```

`result.Err()` is a **grouped summary**, not the first failure, so asserting on it means
matching the sentinel and reading the count — not comparing against one item's error:

```go
if !errors.Is(result.Err(), typesafe.ErrBatchPartialFailure) { ... }
for _, err := range result.Errors() { ... }   // the detail, in input order
```

To test concurrency itself, count in the handler rather than trusting the option.
`WithAdaptiveConcurrency(false)` pins the limit so the peak is a constant:

```go
mu.Lock(); inFlight++; if inFlight > peak { peak = inFlight }; mu.Unlock()
```

### The goroutine leak check

Counting *all* goroutines around a batch does not work. The HTTP transport keeps a read
and a write loop per pooled connection, and `httptest` keeps handler goroutines alive
past the response; both outlive the batch by design and vary with connection reuse. A
`runtime.NumGoroutine()` delta measures them, not the thing under test — this SDK's own
first attempt at the check failed for exactly that reason, against an implementation
that leaked nothing.

Count by name instead:

```go
buf := make([]byte, 1<<20)
n := runtime.Stack(buf, true)
live := strings.Count(string(buf[:n]), "SystemOneBatchSeq")
```

Poll it briefly: the iterator returns once `wg.Wait` has, but a finished goroutine still
needs to be scheduled off. A real leak never clears, so a two-second ceiling is
generous rather than flaky.

---

## Testing typed questions

A typed question embeds the plain one, so anything that accepts a `Question` accepts it,
and the JSON is byte-identical. Tests can therefore compare the two directly:

```go
plainJSON, _ := json.Marshal(typesafe.Choice{...})
typedJSON, _ := json.Marshal(typesafe.TypedChoice[Topic](...))
// equal, byte for byte
```

`Untyped()` converts an answer back, so existing assertions and the `decision` package
keep working against a typed decode:

```go
typed, _ := typesafe.TypedChoiceAnswer[Topic](resp, "department")
reflect.DeepEqual(typed.Untyped(), plain) // true
```

To assert that a mistake is caught at *compile* time, a normal test is the wrong tool —
the file would have to compile to be part of the test binary. Write the program to a
throwaway module and build it, as `tests/typecheck` does:

```go
cmd := exec.Command("go", "build", "./...")
cmd.Dir = tempModuleWithReplaceDirective
cmd.Env = append(os.Environ(), "GOPROXY=off")
```

Two things make that test mean something. Assert on the compiler's actual complaint, not
just on failure — otherwise a malformed program passes for the wrong reason. And check
that the failure is not "could not find module", which would make every case pass
vacuously. Keep a control program that *must* compile beside the negative cases.

---

## Testing generated questions

Check the generated file in, and assert it is current:

```go
// regenerate into a temp file, compare with the committed one
if string(got) != string(want) {
    t.Errorf("out of date; run: go generate ./...")
}
```

Generate into a temp file rather than over the committed one — a failing test must not
leave the tree already changed to match itself.

**Normalise line endings before comparing.** A generator emits LF; git on Windows checks
out CRLF unless told otherwise, so a byte comparison reports the file as out of date on
Windows and only on Windows. `.gitattributes` pins LF everywhere and is the real fix, but
it does not renormalise a working tree that already exists — so the comparison strips
`\r` as well. This repository found that the way everyone does: a green matrix with one
red Windows job. `internal/genexample` does this, then
round-trips the generated questions against a recorded cassette, so the assertion covers
the whole path from enum to wire and back.

---

## Testing with the observability modules

`typesafeotel` is testable without a collector — the OTel SDK ships an in-memory
recorder:

```go
sr := tracetest.NewSpanRecorder()
tp := trace.NewTracerProvider(trace.WithSpanProcessor(sr))
tracer := typesafeotel.New(typesafeotel.WithTracerProvider(tp))
// ... make calls ...
for _, s := range sr.Ended() { ... }
```

Assert parentage on **span ids**, not on names: a test that only checks names passes
happily when every attempt span is an orphan in its own trace.

```go
if s.Parent().SpanID() != call.SpanContext().SpanID() { ... }
```

`typesafeprom` exposes a `prometheus.Collector`, so a test registers it in a fresh
registry and reads the families back:

```go
reg := prometheus.NewRegistry()
reg.Register(metrics)
got, _ := reg.Gather()
```

Use a **new registry per test**. The default one is global, and tests that share it
observe each other's counters.

`typesafecache` takes an injectable clock through its test hooks, so expiry and alias
moves are testable without sleeping. Where a test needs the production behaviour, age
the cache rather than purging it: purging drops cached responses but deliberately keeps
what the cache learned about model aliases, and a test that purges is not exercising
what happens in production.

---

## Testing an integration

Every integration module ships an `example_test.go`. They compile and are type-checked
but carry no `// Output:` comment, so they are **not executed** — running one would need
a live key and, for Temporal, a cluster. A compiled example still catches the thing that
rots fastest: a signature change that makes the documented usage wrong.

For the HTTP middlewares, assert the correlation id reached the **API**, not just the
context. The context assertion passes on a middleware that quietly drops the id:

```go
api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    sent = r.Header.Get("x-correlation-id")   // this is the assertion that matters
    ...
}))
```

For Temporal, `TestActivityEnvironment` tests the Activity against a stub API and
`TestWorkflowEnvironment` runs the workflow end to end. To test determinism, mock the
activity by name — which is what a replay does with a recorded result — and run the
workflow repeatedly:

```go
env.OnActivity(tstemporal.SystemOneActivityName, mock.Anything, mock.Anything).
    Return(fixedResponse, nil)
```

The activity must still be **registered** even when mocked: `OnActivity` resolves the
name against the registry.

For MCP, drive the server through the SDK's own in-memory transport rather than
hand-rolling JSON-RPC. The handshake, the schema inference and the output validation are
then the shipping code paths:

```go
serverT, clientT := mcp.NewInMemoryTransports()
go srv.Run(ctx, serverT)
session, err := mcp.NewClient(...).Connect(ctx, clientT, nil)
```

Worth knowing: the MCP SDK validates a tool's output against the schema it inferred from
the output type — **including a failed call's**. A handler returning a bare zero value
whose map is nil produces JSON `null`, fails validation, and turns a clean tool error
into a protocol error. Return a schema-valid empty value on every error path.

---

## Keeping the documentation true

`docs_example_test.go` holds the code from `docs/MANUAL.md` and the README
verbatim, as an `Example` with no `// Output:` comment — compiled and
type-checked on every run, never executed. A signature change makes the
documentation fail to build rather than quietly making it wrong.

The same idea, one level up: every `examples/*/README.md` shows output copied
from a real run against its cassette, not written by hand. Doing it that way
caught three claims that were wrong about the model's actual behaviour.

---

## Checking questions with the analyzers

Beyond runtime tests, the three `go/analysis` analyzers catch question-design mistakes
at build time: compound questions, documented Jev failure modes, and decisions made
without consulting confidence.

```bash
go install github.com/nibir1/typesafe-go/lint/cmd/typesafe-lint@latest
go vet -vettool=$(which typesafe-lint) ./...
```

They skip `_test.go` by default, since tests construct deliberate edge cases. See
[LINTING.md](LINTING.md).

---

## Checking a request from the command line

For a one-off check without writing a test:

```bash
typesafe lint -f request.json            # validation, references, size
typesafe lint -f request.json --strict   # warnings become a non-zero exit
typesafe estimate -f request.json        # tokens and cost, sends nothing
typesafe replay -f request.json --cassette testdata/cassettes/triage.jsonl
```

`typesafe replay` exits 8 on a cassette miss, which makes it usable as a CI check that a
recorded fixture still matches the request it was recorded for.

---

## Assertions

```go
typesafetest.AssertChoice(t, resp, "department", "technical")
typesafetest.AssertNoulAbove(t, resp, "is_urgent", 0.8)
typesafetest.AssertNoulBelow(t, resp, "is_spam", 0.2)
typesafetest.AssertScoreBetween(t, resp, "frustration", 1.0, 2.0)
typesafetest.AssertConfidenceAtLeast(t, resp, "department", 0.7)
typesafetest.AssertProbabilitiesSumToOne(t, resp)
typesafetest.AssertAnsweredAll(t, resp, "is_urgent", "department")
typesafetest.AssertErrorIs(t, err, typesafe.ErrRateLimit)
typesafetest.AssertAPIStatus(t, err, 429)
```

All report with `Errorf` rather than `Fatalf`, so one wrong answer does not hide the
others in the same response.

`AssertConfidenceAtLeast` on a **Noul** always fails, with a message explaining why: a
Noul carries no confidence, because its probability already expresses the uncertainty.
Asserting on it means the test is reading the wrong field.

---

## Keeping a test suite from spending money

A test that accidentally reaches the live API is a test that fails in CI for unrelated
reasons — and bills you. Two guards:

```go
// Refuse before any network I/O, whatever the test does.
budget := typesafe.NewBudget(typesafe.MaxTotalRequests(0))
client, _ := typesafe.NewClient(typesafe.WithBudget(budget))
```

```go
// Or assert the suite needs no credentials at all.
// env -u TYPESAFE_API_KEY go test ./...
```

This repository's own `make verify` runs the second form, so "the suite is offline" is
checked rather than assumed.

---

## Checking your questions before you send them

Two checks catch mistakes that are otherwise invisible at runtime.

```go
warnings, err := req.Validate()   // err: the server would reject this
                                  // warnings: legal, but probably not intended

for _, w := range req.CheckReferences() {
    t.Errorf("%s", w)             // a backticked path that names nothing
}
```

`CheckReferences` is worth a line in your own test suite. TypeSafe's docs recommend
pointing a question at part of a structured state by backticked path, and the server
does not resolve those — the model just sees a path naming nothing and answers anyway.
Nothing in the response tells you it happened.

---

## Running this repository's own tests

```bash
make verify   # the full offline gate — what CI runs, no key needed
make live     # the above, plus the live API (needs TYPESAFE_API_KEY)
make test     # just the offline unit tests, fast
make flake    # repeat five times under -race to surface flakes
make cover    # cross-package coverage
make bench    # benchmarks; BENCHOUT=f.txt to save for a comparison
```

`make verify` is the gate. It runs, in order:

| Target | Asserts |
|---|---|
| `tidy-check` | `go.mod` is tidy |
| `fmt-check`, `integrations-fmt` | every module is formatted |
| `vet`, `vet-integration` | `go vet` under both build tags |
| `deps`, `deps-graph` | **the core still has zero dependencies** |
| `test-race` | the whole suite under `-race` |
| `contract` | fixtures match the locked wire contract |
| `fixtures` | fixtures validate against the OpenAPI schema |
| `secrets` | no credential in any committed fixture or cassette |
| `docs-check` | 100% doc coverage on exported symbols |
| `links` | every relative link in the docs resolves |
| `mermaid` | every diagram parses |
| `licenses` | every third-party licence permits Apache-2.0 redistribution |
| `bench-check` | every benchmark still builds and runs |
| `dashboards` | the Grafana dashboard references metrics that exist |
| `lint-module`, `submodules`, `integrations`, `examples` | every other module |
| `analyzers` | the analyzers pass over this repository's own source |

Suites are split by what they need:

| Path | Needs | Run by |
|---|---|---|
| `./...` (package tests) | nothing | `make test` |
| `tests/contract/` | nothing | `make contract` |
| `tests/typecheck/` | the Go toolchain | `make test` |
| `examples/` | committed cassettes | `make examples` |
| `tests/integration/` | a live key, `-tags=integration` | `make integration` |

### Linting

```bash
golangci-lint run ./...        # uses the repository's .golangci.yml
```

The config is curated rather than maximal, and one file at the root governs every
module so a rule cannot hold in one place and not another. See
[LINTING.md](LINTING.md#golangci-lint) for what is enabled and why.

## CI

This repository runs everything from a **single workflow**,
`.github/workflows/ci.yml`: the code gate, linting, documentation checks, drift
detection, the live API suite and releases. A `meta` job classifies the run and
every other job states its condition in one line.

For your own project, the shape that matters is smaller:

```yaml
- run: go test -race ./...     # offline, no key needed
```

Gate anything that needs credentials behind a build tag, so the default suite stays
runnable by anyone who clones the repo:

```go
//go:build integration
```

```yaml
- run: go test -tags=integration ./...
  env:
    TYPESAFE_API_KEY: ${{ secrets.TYPESAFE_API_KEY }}
```

Refresh every cassette in one run with `TYPESAFE_UPDATE_CASSETTES=1`, and review the
diff. An empty diff means the API has not drifted.

---

The [user manual](MANUAL.md) covers when to reach for each double; [CONTRIBUTING.md](../CONTRIBUTING.md)
covers running this repository's own gate.
