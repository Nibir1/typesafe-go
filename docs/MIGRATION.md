# MIGRATION.md

Coming from the Python or JavaScript SDK.

---

## The shape is the same

All three send one state and a map of named questions, and get back one answer
per name. If you know the wire, you know this library.

```python
# Python
client.system_one(
    state="Help! My payouts are failing.",
    questions={"is_urgent": Noul(instructions="Does this convey urgency?")},
)
```

```javascript
// JavaScript
await client.systemOne({
  state: "Help! My payouts are failing.",
  questions: { is_urgent: noul({ instructions: "Does this convey urgency?" }) },
});
```

```go
// Go
client.SystemOne(ctx, &typesafe.SystemOneRequest{
    State: "Help! My payouts are failing.",
    Questions: typesafe.Questions{
        "is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
    },
})
```

---

## Configuration defaults

Only the first block is verified. The retry defaults below were read from the
official SDK's source during this project's audit (2026-09-18) and matched
deliberately, so a request that retried three times there retries three times
here:

| | Official SDK | This SDK |
|---|---|---|
| Max retries | 2 | 2 |
| Initial backoff | 500ms | 500ms |
| Backoff cap | 5s | 5s |
| Jitter | 0.25 | 0.25 |
| Retried statuses | 408, 429, 500–599 | 408, 429, 500–599 |
| Overall retry budget | 30s | 30s |

**The rest of the official SDKs' defaults are not reproduced here.** Publishing
a comparison table of someone else's values invites it to be wrong and to go
stale, and you are better served by their own documentation. What follows is
what *this* SDK does, so you can check it against whatever you are migrating
from.

| | This SDK | Set it with |
|---|---|---|
| Base URL | `https://api.typesafe.ai` | `WithBaseURL`, `TYPESAFE_BASE_URL` |
| API key | `TYPESAFE_API_KEY` | `WithAPIKey` |
| Default model | `jev-latest` | `WithDefaultModel`, `TYPESAFE_DEFAULT_MODEL` |
| Per-attempt timeout | 60s | `WithTimeout` |
| Whole-call timeout | 30s incl. backoff | `RetryPolicy.Timeout` |
| Context-limit check | on | `WithContextLimitCheck(false)` |
| Client-side rate limit | off | `WithBudget` |
| Circuit breaker | off | `WithCircuitBreaker` |
| Response cache | off | `typesafecache` |

### Three that differ from the other SDKs in kind, not in value

**A request over the context limit fails locally.** Before anything is sent,
`EstimateTokens` checks both ceilings and the error names which one you crossed
and which question did it. The alternative — send it and let the API return a
422 — costs a round trip to learn something arithmetic could have told you.

**There are two timeouts, and you want both.** `WithTimeout` bounds one
attempt; `RetryPolicy.Timeout` bounds the whole call including backoff. Set
only the first and a retried call can outlive the request that started it.

**Rate limiting, circuit breaking and caching are available and off.** They are
opt-in because they change behaviour in ways that should be a decision, not a
default you discover during an incident.

---

## Errors

Python raises; Go returns.

```python
try:
    resp = client.system_one(...)
except RateLimitError as e:
    retry_after = e.retry_after
```

```go
resp, err := client.SystemOne(ctx, req)

var rl *typesafe.RateLimitError
if errors.As(err, &rl) {
    retryAfter := rl.RetryAfter
}

// or, when you only need the class
if errors.Is(err, typesafe.ErrRateLimit) { ... }
```

Every documented failure has a type: `AuthenticationError`,
`PermissionDeniedError`, `NotFoundError`, `UnprocessableEntityError`,
`RateLimitError`, `OverloadedError`, `InternalServerError`, `ConnectionError`,
`TimeoutError`, `ResponseValidationError`.

`APIError.RequestID` is the id **TypeSafe** assigned — the one to quote in a
support ticket. `typesafe.RequestIDFrom(ctx)` is the one *you* generated. They
are different, deliberately.

---

## Answers

Python returns dynamically typed objects. Go decodes into concrete types, and
the accessor you use says which one you expect:

```go
urgent, err := resp.Noul("is_urgent")    // NoulAnswer
team, err := resp.Choice("team")         // ChoiceAnswer
severity, err := resp.Score("severity")  // ScoreAnswer
```

A wrong accessor returns `ErrWrongAnswerType` rather than panicking. When the
type is not known statically, `resp.Answer(id)` discovers it from the wire
discriminator and returns the sealed `Answer` interface, so a type switch over
it is exhaustive.

### The one that catches everybody

**A `Noul` has no confidence field.** In Python you might reach for
`answer.confidence` on anything. Here `NoulAnswer` does not have one, because
the probability *is* the uncertainty. `Answer.Confidence()` returns
`(float64, bool)` and the `bool` is false for a Noul — code that discards it
reads every Noul as maximally uncertain, which the `confidencecheck` analyzer
reports.

---

## What this SDK adds

Claims about what the Python and JavaScript SDKs do or do not have are
deliberately absent — check their documentation rather than trusting a table
here. The audit that backs this project covered the **Go** field (six SDKs,
2026-09-18), and where that audit is the source it is cited.

**Compile-time typed questions.** Parity with the JavaScript SDK's
`ChoiceQuestion<T>`, which already ships this:

```go
type Topic string
const TopicBilling Topic = "billing"

q := typesafe.TypedChoice[Topic]("Which team?",
    typesafe.OptionOf(TopicBilling, "Payments, invoicing, refunds"))

ans, _ := q.Answer(resp, "team")
switch ans.Choice { case TopicBilling: }  // Topic, not string
```

**The `decision` package** — probability algebra, weighted policies with an
audit trace, confidence bands, and weight calibration from labelled data. Also
**0 of 6** in the Go audit.

**Static analysis.** Three `go/analysis` analyzers catching compound questions,
documented Jev failure modes, and decisions made without consulting confidence.

**Cassettes.** Record once against the live API, replay offline forever. Every
example in this repository is tested this way.

**A batch API** that batches states with per-item error isolation, input
ordering and globally adaptive concurrency. The Go audit found this in **0 of
6** competing Go SDKs.

---

## Idioms worth adopting

**Pass a context, and pass the right one.** In an HTTP handler that is
`r.Context()`, so an abandoned request stops spending tokens.

**Build one client for the process.** `*Client` is safe for concurrent use. A
per-request client discards the connection pool, the circuit breaker's state
and the budget's accounting, none of which mean anything within one request.

**Check the answer type you get back.** `errors.Is(err, ErrNoSuchAnswer)` is a
real case: a question id that does not match is a typo the compiler cannot see.

**Run the analyzers.** They catch question-design mistakes at build time, which
is the only place they *can* be caught — nothing in a response says the question
was unsuitable.

---

## A worked conversion

```python
# Python
from typesafe import TypeSafe, Choice, Noul

client = TypeSafe(timeout=30.0, max_retries=3)

resp = client.system_one(
    state=ticket,
    questions={
        "team": Choice(
            instructions="Which team should handle this?",
            criteria={"billing": "Payments", "technical": "Bugs"},
        ),
        "is_urgent": Noul(instructions="Does this convey urgency?"),
    },
)

if resp.answers["team"].confidence > 0.8:
    route(resp.answers["team"].choice)
```

```go
// Go
client, err := typesafe.NewClient(
    typesafe.WithTimeout(30*time.Second),
    typesafe.WithMaxRetries(3),
)
if err != nil {
    return err
}

resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
    State: ticket,
    Questions: typesafe.Questions{
        "team": typesafe.Choice{
            Instructions: "Which team should handle this?",
            Criteria:     typesafe.Options{"billing": "Payments", "technical": "Bugs"},
        },
        "is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
    },
})
if err != nil {
    return err
}

team, err := resp.Choice("team")
if err != nil {
    return err
}
if team.Confidence > 0.8 {
    route(team.Choice)
}
```

Longer, and every failure is visible. That is the trade Go makes everywhere
else too.

---

The [user manual](MANUAL.md) is the full walkthrough; [DECISION_GUIDE.md](DECISION_GUIDE.md)
covers question design, which is where the two SDKs differ least and matter most.
