# OBSERVABILITY.md

Three optional modules: tracing, metrics, and a response cache. None of them is
reachable from the core — each is a **separate Go module**, which is what keeps
`go get github.com/nibir1/typesafe-go` a zero-dependency install.

| Module | Brings in | Go floor |
|---|---|---|
| `typesafecache` | nothing | 1.23 |
| `typesafeotel` | `go.opentelemetry.io/otel` | 1.25 |
| `typesafeprom` | `github.com/prometheus/client_golang` | 1.25 |

The two instrumentation modules sit above the core's floor because their
dependencies declare it. That constrains the module, not you: nothing stops a
Go 1.23 program from using the SDK without them.

---

## Wiring all three

```go
tracer := typesafeotel.New()
metrics := typesafeprom.New()
cache, _ := typesafecache.New(typesafecache.WithTTL(10 * time.Minute))

client, err := typesafe.NewClient(
    typesafe.WithInterceptor(
        tracer.Interceptor(),   // outermost
        metrics.Interceptor(),
        cache.Interceptor(),    // innermost
    ),
    typesafe.WithRetryObserver(func(ctx context.Context, a typesafe.AttemptInfo) {
        tracer.RetryObserver()(ctx, a)
        metrics.RetryObserver()(ctx, a)
    }),
)
```

**Order is a decision, not a detail.** Interceptors run outermost-first, so with
the cache innermost a cached call still produces a span and a latency
observation — it just shows up as a very fast one, which is what you want on a
dashboard. Put the cache outermost instead and a hit becomes invisible to both.

`deploy/example` is this wiring in full, running against Jaeger, Prometheus and
Grafana via `deploy/docker-compose.yml`.

---

## `typesafeotel`

```
typesafe.systemone                    412ms
├── typesafe.attempt 1                 38ms  error, 429
├── typesafe.attempt 2                 41ms  error, 429
└── typesafe.attempt 3                310ms  ok
```

Install **both** the interceptor and the retry observer. Without the observer
you still get the call span, but a retried call is one long bar with no
explanation — the difference between "the API was slow" and "we were rate
limited twice".

Attempt spans are created after the fact with explicit timestamps, because an
attempt is only observable once it has ended and the *successful* one is never
reported by the observer at all. It is closed out by the interceptor instead.

### Semantic conventions, and where they stop fitting

`gen_ai.system`, `gen_ai.request.model`, `gen_ai.response.model`,
`gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`.

Three places the conventions do not describe this API:

- **Output tokens are reported but not billed**, and do not mean "generated
  text" — System One returns probabilities, not completions. A cost panel
  summing `gen_ai.usage.output_tokens` is measuring nothing.
- **No `gen_ai.operation.name` fits.** The conventions enumerate chat,
  text_completion and embeddings. The value is `systemone`.
- **Confidence has no equivalent.** It is the most useful number in a System One
  response, so it goes under `typesafe.answer.confidence.<id>` rather than being
  dropped for the sake of conformance.

A `Noul` has no confidence — its probability *is* the uncertainty — so it is
recorded as `typesafe.answer.noul.<id>` instead. Recording a confidence for a
Noul would be inventing a number.

---

## `typesafeprom`

`Metrics` is a single `prometheus.Collector`, so it registers as one unit:

```go
reg := prometheus.NewRegistry()
reg.MustRegister(metrics)
http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
```

| Metric | Type | Labels |
|---|---|---|
| `typesafe_request_duration_seconds` | histogram | model, outcome |
| `typesafe_answer_confidence` | histogram | question_type, question_id |
| `typesafe_retries_total` | counter | status |
| `typesafe_errors_total` | counter | class |
| `typesafe_questions_total` | counter | type |
| `typesafe_tokens_total` | counter | kind, model |
| `typesafe_batch_items_total` | counter | outcome |
| `typesafe_cache_events_total` | counter | result |
| `typesafe_requests_in_flight` | gauge | — |

### The panel worth having

**Watch the p10 of `typesafe_answer_confidence`.** A drift in the confidence
distribution is the earliest visible sign that the inputs have changed shape. It
moves well before anyone notices the answers are wrong, and long before latency
or the error rate react. Nothing in an ordinary API client has an equivalent.

```promql
histogram_quantile(0.10, sum by (le) (rate(typesafe_answer_confidence_bucket[15m])))
```

### Labels are bounded on purpose

`typesafe_errors_total` is labelled by **class**, not by message. A label built
from `err.Error()` would carry request ids straight into the label set and
create a new time series per failure, which is how a Prometheus server runs out
of memory.

`question_id` is the one caller-controlled label, and it is bounded in practice
because question ids are written in your code. If you build them dynamically,
turn it off with `WithQuestionIDLabel(false)`.

The model label comes from the **response**, not the requested alias. Labelling
by the alias would put two model versions in one series across an alias move and
hide the step change it caused.

---

## `typesafecache`

Only input tokens are billed, and every request carries the whole state, so
re-evaluating the same content is the largest avoidable cost in a typical
integration.

```go
cache, err := typesafecache.New(
    typesafecache.WithTTL(10*time.Minute),
    typesafecache.WithMaxEntries(10_000),
    typesafecache.WithDisk("/var/cache/typesafe"),  // optional, survives restarts
)
```

### The correctness problem it is built around

Jev is documented as highly consistent but is **not contractually
deterministic**, and `jev-latest` is an alias that moves without notice. A cache
keyed on the alias keeps serving answers from the previous model version after a
move, silently — nothing in a cached response says which model produced it.

So the key is built from the **resolved** model id the API reported, never the
requested alias. The alias mapping is tracked separately and learned from each
response; when an alias starts resolving elsewhere, every key under the old id
becomes unreachable at once.

```go
typesafecache.WithObserver(func(e typesafecache.Event) {
    if e.AliasMoved {
        log.Printf("jev alias moved to %s; cache invalidated", e.Model)
    }
})
```

**The window this leaves:** while a cached alias mapping is still live, a move is
not noticed. That window is the TTL, which is what a TTL means. Pin an exact
model id — `jev-1.13.0` rather than `jev-latest` — and the question does not
arise.

### What is not cached

**Failures.** A cached error is a cached outage: a rate limit or a 500 describes
the moment, not the request, and replaying it after the incident is over turns a
transient failure into a permanent one.

### Hits copy by default

A `*SystemOneResponse` handed to two goroutines is shared mutable state. The
default returns a copy — three allocations: the struct, the answer map and its
bucket array. `WithSharedResponses()` returns the stored response itself, which
is allocation-free, for hot paths that treat responses as read-only.

Deriving the key is not free either way: it canonicalizes the request and hashes
it, which cannot be done without touching the heap. There is no
allocation-free cached call, only an allocation-free lookup.

---

## The demo stack

```bash
cd deploy
export TYPESAFE_API_KEY=...
docker compose up
```

- Grafana `http://localhost:3000` — provisioned with the committed dashboard
- Jaeger `http://localhost:16686`
- Prometheus `http://localhost:9090`

The example service makes a call every three seconds from a small pool of
sample tickets, so the panels have something in them and the cache has repeats
to hit. A dashboard of empty panels tells you nothing about whether the wiring
works.

`deploy/grafana/typesafe-systemone.json` is validated by `make dashboards` and
in CI: the checker reads metric names out of `typesafeprom/prom.go`, so renaming
a metric and forgetting the dashboard is a build failure rather than a panel
that quietly reads zero.

---

See the [user manual](MANUAL.md#10-going-to-production) for the production checklist
this fits into.
