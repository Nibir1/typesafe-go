# Changelog

Notable changes, in the format of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
following [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This is written by hand. A list of commit subjects is a log, not a changelog: it
records what was typed, not what changed for you.

## [Unreleased]

Nothing yet.

## [1.0.0] — 2026-09-19

The first release. Everything below is new, so rather than an inventory of every
symbol — [pkg.go.dev](https://pkg.go.dev/github.com/nibir1/typesafe-go) does that
better — this records what the SDK does and the decisions worth knowing about.

### The client

- `Client` with functional options, safe for concurrent use, built once per
  process.
- Typed errors for every documented failure: `AuthenticationError`,
  `PermissionDeniedError`, `NotFoundError`, `UnprocessableEntityError`,
  `RateLimitError`, `OverloadedError`, `InternalServerError`, `ConnectionError`,
  `TimeoutError`, `ResponseValidationError` — reachable with `errors.As`, and
  matching sentinels with `errors.Is`.
- Retries with exponential backoff and jitter, honouring `Retry-After` up to a
  cap, plus an optional circuit breaker.
- **The wire contract was established empirically**, against the live API and the
  OpenAPI document rather than from prose. Nineteen corrections are recorded in
  [docs/WIRE_CONTRACT.md](docs/WIRE_CONTRACT.md), including a `Score` minimum of
  1 rather than the documented 2, an undocumented maximum of 10, and three
  distinct shapes of the `detail` field.

### The three primitives

- `Noul`, `Choice` and `Score`, with typed answers.
- `Noul` has **no confidence field**, because the probability is the uncertainty.
  `Answer.Confidence()` returns `(float64, bool)` for that reason.
- `Score` accessors sort level keys numerically, not lexicographically.
- Construction-time validation for everything the server is known to reject.

### Deciding, not just asking

- `decision` — probability algebra (`All`, `Any`, `AtLeast`, `Expected`),
  weighted policies with an audit trace, confidence bands, and weight
  calibration from labelled data.
- `Calibrate` reports whether your questions separate your labels at all. If
  they do not, no weighting rescues them.

### Cost and limits

- `EstimateTokens` checks **both** context ceilings before sending — 64,000 for
  the whole request, 32,000 for the state plus the single longest question. The
  second is the one that surprises people.
- The token model is measured, not a tokenizer, and over-reports by design.
- `Budget` caps requests, tokens and lifetime spend per process.

### Scale

- `SystemOneBatch` — bounded worker pool, per-item error isolation, results in
  input order, globally adaptive concurrency on `429`.
- `SystemOneBatchSeq` — an `iter.Seq2` streaming view. Breaking out cancels the
  batch; no goroutine outlives the loop.
- It batches **states, never questions**: Jev ingests the state once and
  evaluates every question in parallel, so a second question is nearly free
  while a second state is a whole request.

### Type safety

- `TypedChoice[T]` and `TypedScore[L]` over your own enums, with `Exhaustive`
  for the drift types cannot catch.
- `typesafe-gen` generates questions and accessors from a tagged struct.

### Catching mistakes at build time

- Three `go/analysis` analyzers: `atomicquestion`, `jaggededge`,
  `confidencecheck`. Every `jaggededge` rule cites the section of TypeSafe's
  published jaggedness notes it comes from.
- They match composite literals only. Questions built with constructors are
  **not** linted — a known gap, recorded in the roadmap.

### Testing

- `typesafetest` — mock client, test server, assertions.
- `cassette` — record once against the live API, replay offline forever. Every
  example and several integration tests use it.

### Observability and caching

- `typesafeotel` — a span per call and per attempt, GenAI semantic conventions
  where they apply, with the three places System One does not fit them
  documented.
- `typesafeprom` — nine collectors, including an answer-confidence histogram.
- `typesafecache` — LRU with an optional disk tier, keyed on the **resolved**
  model id rather than the requested alias, so a moved alias invalidates cleanly
  instead of serving stale answers.

### Integrations

`nethttp`, `gin`, `echo`, `fiber`, `langchaingo`, `temporal`, and an MCP server
whose `evaluate_policy` tool lets an agent name a policy without being able to
see or change it.

### Tooling

- `typesafe` CLI: run, estimate, lint, explain, models, doctor, replay, record,
  completion, version.
- Ten runnable examples, each replayed against a cassette in CI.

### Known gaps at 1.0

- **The analyzers do not see constructor-built questions.** They match composite
  literals, so `NewChoice(…)`, `TypedChoice[T](…)` and generated questions are
  missed. Closing it means teaching `lint/internal/qast` to recognise calls.
- **Temporal replay is verified in the test environment, not against a recorded
  history.** Determinism is asserted across repeated runs with the activity
  mocked; a true `WorkflowReplayer` test needs an event history, which needs a
  real cluster.
- **`SuggestedFix` is not implemented.** Splitting a compound question needs a
  human decision about what the two questions should be.

### Verified against the live API

The wire contract, the token model and every example's cassette were recorded
against the real API, not constructed. `make live` re-verifies the contract.

[Unreleased]: https://github.com/nibir1/typesafe-go/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/nibir1/typesafe-go/releases/tag/v1.0.0
