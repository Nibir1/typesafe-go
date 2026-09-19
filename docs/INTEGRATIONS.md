# INTEGRATIONS.md

Seven modules that put TypeSafe where you already are. Each has its own
`go.mod`, so importing the SDK never drags any of them in — and `make
deps-graph` asserts the core's dependency graph is still empty.

| Module | Brings in | Go floor |
|---|---|---|
| `integrations/nethttp` | nothing — stdlib only | 1.23 |
| `integrations/gin` | `github.com/gin-gonic/gin` | 1.25 |
| `integrations/echo` | `github.com/labstack/echo/v4` | 1.25 |
| `integrations/fiber` | `github.com/gofiber/fiber/v3` | 1.25 |
| `integrations/langchaingo` | `github.com/tmc/langchaingo` | 1.24 |
| `integrations/temporal` | `go.temporal.io/sdk` | 1.26 |
| `integrations/mcp` | `github.com/modelcontextprotocol/go-sdk` | 1.25 |

Most sit above the core's 1.23 because their dependencies declare it. That
constrains the integration, not you: the core and `nethttp` still build on 1.23,
and nothing stops a 1.23 program from using the SDK without them.

---

## HTTP: `nethttp`, `gin`, `echo`, `fiber`

All four do the same two things.

```go
handler := nethttp.CorrelationMiddleware()(nethttp.Middleware(client)(mux))
```

```go
r.Use(tsgin.Middleware(client), tsgin.Correlation())      // gin
e.Use(tsecho.Middleware(client), tsecho.Correlation())    // echo
app.Use(tsfiber.Middleware(client), tsfiber.Correlation()) // fiber
```

**Client injection** is the thin half, and on its own would not justify a
module. The client goes on the *request's* context rather than only in the
framework's own store, so it travels into anything that takes a
`context.Context` — a repository call, a background span, a helper that knows
nothing about your web framework.

**Correlation is the half worth having.** It carries the inbound request id into
the SDK, so one id ties the HTTP request, this SDK's logs and TypeSafe's own
records together:

```
X-Request-Id: abc-123   →   x-correlation-id: abc-123 on the API call
```

During an incident the first question is always which inbound request produced
a given answer. Without this, every call invents its own id and correlating them
means joining on timestamps.

Headers are checked in order — `X-Request-Id`, `X-Correlation-Id`, `traceparent`
— most specific first: an id a proxy set for this hop beats the trace-wide
`traceparent`. A request with no correlation header is left alone, so the
client's own `WithRequestID` generator still applies rather than the id being
blanked out.

**Always pass the request's context to `SystemOne`.** It carries the client and
the correlation id, and it is cancelled when the caller hangs up — which is what
stops an abandoned request from continuing to spend tokens. In Fiber that means
`c.Context()`, not `context.Background()`.

---

## `langchaingo`

```go
classifier := tslangchain.NewClassifier(client, typesafe.Questions{
    "team":      typesafe.Choice{...},
    "is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
},
    tslangchain.WithName("classify_support_ticket"),
    tslangchain.WithDescription("Classify a support ticket. Input: the ticket text."),
)

agent := agents.NewOneShotAgent(llm, []tools.Tool{classifier})
```

An agent asked to classify something does it by generating text and hoping the
text parses. This replaces that with a probability over a set you defined: the
answer cannot be a value nobody declared, and it arrives with a confidence.

**Name and describe the tool for the decision, not the vendor.** The description
is the only thing telling the model when the tool applies and what to put in it.
`typesafe_classify` tells it nothing; `classify_support_ticket` does. A
description is generated from the questions when you give none — serviceable,
but it describes the questions rather than when to use them.

**Two types, not one.** `tools.Tool` declares `Call(ctx, string) (string, error)`
and `chains.Chain` declares `Call(ctx, map, ...opt) (map, error)`. Same name,
different signatures, so no single Go type can satisfy both. `Classifier` is the
tool, `Chain` is the chain, and they share everything else.

The tool returns JSON, because that is the only shape an agent can reliably act
on — including confidence, since a tool that returns only the winning option
throws away the number that decides whether to act on it. A `Noul` has no
confidence, so the field is **absent** rather than zero, which would read as
"completely unsure".

---

## `temporal`

**The API call goes in an Activity. Never in workflow code.**

Workflow code is replayed — Temporal re-executes it from the event history after
a worker restart, a deploy, or a continue-as-new, and it must produce the same
commands every time. An HTTP call is not replayable: the second run would make a
second call, get a different answer (Jev is documented as highly consistent but
is not contractually deterministic), and diverge. An Activity's *result* is
recorded in the history and handed back on replay without re-running anything.
That is not a style preference; it is the only arrangement that works.

```go
func TriageWorkflow(ctx workflow.Context, ticket string) (string, error) {
    in, err := tstemporal.NewInput(ticket, questions)   // marshals only; safe here
    if err != nil {
        return "", err
    }
    resp, err := tstemporal.ExecuteSystemOne(ctx, in)   // the Activity
    ...
}

w := worker.New(c, "triage", worker.Options{})
tstemporal.Register(w, tstemporal.NewActivities(tsClient))
```

### Retries belong to one layer

The SDK retries internally and Temporal retries Activities. Left alone they
multiply: three SDK attempts inside three Temporal attempts is nine calls for one
logical request, and the Activity's start-to-close timeout has to cover the SDK's
own backoff before Temporal sees a failure at all.

Pick Temporal — its retries survive a worker crash and are visible in the UI:

```go
tsClient, _ := typesafe.NewClient(typesafe.WithRetryPolicy(typesafe.NoRetry()))
```

`DefaultActivityOptions` is built for that arrangement, and the Activity converts
the failures a retry cannot fix — a bad key, a malformed request — into
non-retryable errors so Temporal stops immediately instead of burning the whole
policy on a request that can never work.

### Two things to know

**Questions are serialized as `RawQuestion`.** `typesafe.Question` is a sealed
interface and Temporal deserializes the input on the worker side, where there is
no concrete type to decode into. `NewInput` converts; a test asserts the
converted questions marshal to the same bytes the typed forms would, so nothing
changes on the wire.

**Budgets do not span workers.** A `typesafe.Budget` counts in one process. With
several workers each enforces the cap independently and the account sees the sum.
Size it per worker, or use a Temporal task-queue rate limit, which is
cluster-wide.

---

## `mcp`

```bash
go install github.com/nibir1/typesafe-go/integrations/mcp/cmd/typesafe-mcp@latest
```

```json
{
  "mcpServers": {
    "typesafe": {
      "command": "typesafe-mcp",
      "args": ["-policies", "/etc/typesafe/policies", "-only-policies"],
      "env": { "TYPESAFE_API_KEY": "..." }
    }
  }
}
```

Three tools:

| Tool | What it does |
|---|---|
| `systemone` | Ask Noul, Choice and Score questions about text |
| `models` | List the models the account may use |
| `evaluate_policy` | Run a named `decision` policy and return a verdict with its arithmetic |

### `evaluate_policy` is the reason this exists

A thin MCP proxy over the HTTP API lets an agent ask anything. `evaluate_policy`
inverts that: **the agent names a policy and supplies text**. The questions, the
weights and the thresholds belong to the server. The agent never sees them,
cannot drift from them, and cannot be talked out of them by the text it is
judging.

What comes back is a verdict — `allow`, `warn`, `review`, `block` — and the
arithmetic behind it:

```json
{
  "policy": "moderation",
  "verdict": "review",
  "score": 0.6833,
  "contributions": [
    {"question": "is_abusive", "weight": 2, "value": 0.62, "contribution": 1.24},
    {"question": "is_spam",    "weight": 1, "value": 0.81, "contribution": 0.81}
  ]
}
```

Quote the contributions when explaining the outcome. A verdict without them is an
assertion.

Policies are plain JSON files, one per policy, loaded with `-policies <dir>`:

```json
{
  "policy": {
    "name": "moderation",
    "weights": { "is_spam": 1, "is_abusive": 2 },
    "normalize": true,
    "review_above": 0.5,
    "block_above": 0.9
  },
  "questions": {
    "is_spam":    { "type": "noul", "instructions": "Is this message spam?" },
    "is_abusive": { "type": "noul", "instructions": "Is this message abusive towards a person?" }
  },
  "description": "Moderate user-submitted text. A block verdict means do not publish."
}
```

A policy weighing a question its file does not define is rejected **at load**,
where an operator can fix it, rather than mid-conversation where an agent cannot.

### The trust boundary

Everything an MCP client sends is untrusted input. It arrives as the `state` of a
question — data to be judged, never instructions — and the questions themselves
come from the server's configuration, not from the request.

`-only-policies` removes the free-form `systemone` tool, so the server exposes
nothing but the policies you defined. That is the shape to deploy when the agent
should not be able to ask arbitrary questions.

### Errors

A domain failure — a rate limit, a malformed question, an unknown policy — is
reported as a **tool error** (`isError` on the result), not a protocol error. A
protocol error means a broken server, and a client treats the two differently:
it retries one and gives up on the other.

---

## Testing an integration

Every module ships an `example_test.go`. They compile and are type-checked but
carry no `// Output:` comment, so they are **not executed** — running them would
need a live key and, for Temporal, a cluster.

The tests that do run use `httptest` or a cassette. The pattern worth copying is
asserting the correlation id reached the *API*, not just the context:

```go
api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    sent = r.Header.Get("x-correlation-id")   // assert on this
    ...
}))
```

`make integrations` runs all seven.

---

See the [user manual](MANUAL.md) for the SDK itself, and [examples/](../examples) for
patterns you can copy.
