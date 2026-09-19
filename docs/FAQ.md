# FAQ.md

---

### Is this an official TypeSafe SDK?

No. It is a community SDK, Apache-2.0, not affiliated with or endorsed by
TypeSafe AI. "TypeSafe", "System One" and "Jev" are their names, used here only
to describe what this interoperates with.

### Why does importing it pull in nothing?

Because the core has no third-party dependencies, and `make deps-graph` plus a
CI job assert it on every change. Analyzers, tracing, metrics, caching and every
integration live in separate modules, so you take only what you ask for.

### What Go version do I need?

**1.23** for the SDK, the CLI and the `decision` package. Some optional modules
sit higher because their dependencies declare it — 1.26 for the Temporal
integration, 1.25 for OpenTelemetry, Prometheus and the web frameworks. That
constrains those modules, not you.

---

### Why is the answer a probability instead of a boolean?

Because the model's uncertainty is information, and a boolean throws it away.
`0.51` and `0.99` become the same `true`, and you lose the ability to treat them
differently — which is exactly what you want to do.

The threshold is a product decision: the point where you would rather be wrong
one way than the other. Putting it in your code rather than the model's is the
whole idea.

### Why does a `Noul` have no confidence?

The probability *is* the uncertainty. `0.5` means genuinely undecided. A
confidence alongside it would be asking the model how sure it is about how sure
it is.

`Choice` and `Score` do have one, because "which option won" and "how clearly it
won" are genuinely separate questions there.

### Why does `Confidence()` return two values?

`(float64, bool)` — and the `bool` is false for a `NoulAnswer`, which has none.
Code that discards it reads every Noul as maximally uncertain. The
`confidencecheck` analyzer reports exactly that.

### Should I include a catch-all option?

It changes the answer more than most people expect. Measured on the same input:
with an `unclear` option, `unclear` at **1.00**; without it, a wrong option at
**0.47**.

A catch-all absorbs ambiguity and raises confidence. Include one when "none of
these" is an outcome you can act on; leave it out when you would rather see
ambiguity surface as low confidence. See
[`confidence_routing`](../examples/confidence_routing).

### Can I compare `Score` values numerically?

No. The ordering is reliable; the spacing is not. `AtOrAbove(2)` is sound,
`Score > 2.5` is not. If you need a real number, extract it as a `Choice` over
enumerated ranges and compute in code. [DECISION_GUIDE.md](DECISION_GUIDE.md)
has the detail.

---

### How do I test without an API key?

Three ways, in [TESTING.md](TESTING.md): a mock, a test server, or a cassette
recorded once against the live API and replayed forever after. Every example in
this repository uses the third.

```go
client, _ := typesafe.NewClient(
    typesafe.WithAPIKey("test"),
    typesafe.WithHTTPClient(cassette.MustReplay("testdata/cassettes/triage.jsonl")),
)
```

### Will a cassette leak my API key?

No. Cassettes scrub configured secrets on write, `cassette.Verify` checks a file,
and `make secrets` scans **every** `testdata` tree in the repository on each run.
That scan used to look at two hard-coded paths and missed a cassette stored
beside the package that used it; it now walks them all.

### Why do my re-recorded cassettes give different numbers?

Jev is documented as highly consistent but is **not** contractually
deterministic. One example here scored 0.75 on one run and 0.38 on another with
the same input and question set.

Do not write a test that asserts an exact probability. Assert the shape, the
band, or the decision.

---

### The SDK is slow / how much does it add?

~35 µs per call, against an API documented at 70–500 **ms**. That is 0.05% of
the fastest possible response. Measured, with the methodology, in
[PERFORMANCE.md](PERFORMANCE.md).

### Why is my request rejected before it is sent?

You crossed one of two context ceilings — 64,000 tokens for the whole request,
or 32,000 for the state plus the single longest question. The second is the
surprising one: the state counts once for the total and again for *each*
question.

```bash
typesafe estimate -f request.json
```

Turn the check off with `WithContextLimitCheck(false)` if you would rather the
API decide.

### How do I stop this costing me money?

- Only input tokens are billed; the state is the expensive part and is sent in
  full every time.
- Pack questions into one call; a second *state* costs a whole request.
- `WithBudget(NewBudget(MaxTotalTokens(n)))` is a hard stop per process.
- [`typesafecache`](../typesafecache) skips re-evaluating content you have
  already seen.

### Do I need the cache?

Only if you evaluate the same content twice. If you do, it is the largest
saving available. Read the model-alias section of
[OBSERVABILITY.md](OBSERVABILITY.md) first — it keys on the **resolved** model
id rather than the alias, deliberately.

---

### One client or one per request?

One per process. `*Client` is safe for concurrent use, and a per-request client
discards the connection pool, the circuit breaker's state and the budget's
accounting — all of which only mean anything across requests.

### Why is there no `client.Use(middleware)`?

Because mutating a live client is a data race waiting to happen. Interceptors
compose at construction, which makes the chain immutable. The set of
interceptors is a deployment decision, not a per-call one.

### Interceptors or hooks?

Hooks to observe, interceptors to alter. Hooks are implemented as an
interceptor, so they nest in the order everything was declared.

### Why does an interceptor see one call when three HTTP requests happened?

Because retries happen *beneath* the innermost handler. That is almost always
what instrumentation wants: a latency histogram should record what the caller
waited for, not one bar per attempt. For per-attempt visibility use
`WithRetryObserver`.

---

### Why don't the analyzers catch questions built with constructors?

They match composite literals only, so `NewChoice(...)`, `TypedChoice[T](...)`
and generated questions are currently missed. It is a real hole — recorded in
the roadmap, and the next thing to do in that module. Questions written as
struct literals are fully covered.

### What does `//nolint` need?

A reason. A bare `//nolint` is itself reported, because why a suppression was
added is exactly what the next reader needs.

```go
//nolint:jaggededge counts are bounded at three here and verified in code
```

---

### Can I use this with an agent framework?

Yes — LangChainGo, Temporal and MCP integrations all ship. See
[INTEGRATIONS.md](INTEGRATIONS.md).

The MCP server's `evaluate_policy` tool is worth a look: the agent names a
policy and supplies text, while the questions, weights and thresholds stay on
the server. The agent cannot see them, drift from them, or be talked out of
them by the text it is judging.

### What does `v1.0` actually promise?

No exported symbol outside `internal/` is removed, renamed or changed in
signature during 1.x. Anything to be removed is marked `// Deprecated:` for at
least one minor cycle first, naming its replacement.

Three things are explicitly not covered: `internal/`, behaviour that depends on
the API's answers (a probability is not a contract), and the wire-contract
corrections, which describe what the server does today. Full statement in the
[README](../README.md#stability).

### Which Go versions are supported?

The core supports the current stable release and the four before it — today
1.23 through 1.27. Raising the floor is a minor-version event, announced one
cycle ahead.

Optional modules sit higher where a dependency forces it: 1.24 for
`langchaingo`, 1.25 for OpenTelemetry, Prometheus, the web frameworks and MCP,
1.26 for Temporal and the analyzers. That constrains those modules, not you.

### Why is a submodule tagged `typesafecache/v1.0.0` and not `v1.0.0`?

Because it is a separate Go module, and that is how Go tags one in a
subdirectory. Each versions independently, so a breaking change in the Temporal
integration does not force a major bump on the cache.

If `go get` of a very new submodule tag fails, the module proxy may not have
seen it yet.

### How is a release cut?

```bash
make release VERSION=v1.0.0 CONFIRM=yes
```

It runs the full gate, tags the root, waits for the module proxy, re-points the
eleven submodules at the published version, tags those, and restores the
development state. The GitHub release body is the matching section of
[Release_Notes.md](../Release_Notes.md).

Dry run is the default. Pushing a tag is not undoable: the proxy caches a
version within minutes and deleting the tag does not unpublish it.

### Are releases signed?

Yes — keyless Sigstore, so there is no private key to leak and the certificate
records which workflow in which repository produced the binary. An SBOM ships
with each archive and build provenance is attested. The verification commands
are in [SECURITY.md](../SECURITY.md#supply-chain).

### How do I contribute?

`make verify` has to pass — that is the gate CI runs. It covers formatting,
vet under both build tags, the zero-dependency assertion, race tests, the wire
contract, fixture validation, a secret scan, 100% doc coverage on exported
symbols, the analyzers against this repository's own source, every submodule,
every integration and every example.

```bash
make verify        # offline, no key needed
make live          # the above, plus the real API
```

---

Not here? The [user manual](MANUAL.md) is the long form, and
[WIRE_CONTRACT.md](WIRE_CONTRACT.md) has the details of what the API really does.
