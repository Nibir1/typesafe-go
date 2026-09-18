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

## CI

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
