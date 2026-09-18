# WIRE_CONTRACT.md

Authoritative wire contract for the TypeSafe System One API, as implemented by
`github.com/nibir1/typesafe-go`.

| | |
|---|---|
| **Locked** | 2026-09-18 |
| **Verified against the live API** | 2026-09-18 — all 10 fixtures replayed successfully; error envelopes captured |
| **Machine-readable source** | [`testdata/spec/openapi.json`](../testdata/spec/openapi.json) — OpenAPI 3.1.0, `TypeSafe` v0.2.0, served live at `https://api.typesafe.ai/openapi.json` |
| **Prose source** | `https://docs.typesafe.ai/api`, `/primitives`, `/primitives/advanced`, `/concepts/state`, `/models` |
| **Executable form** | [`testdata/contract/`](../testdata/contract/) — 10 golden request/response pairs, all schema-validated and all replayed live |
| **Drift detection** | `go test -tags=integration -run TestContractDrift ./...` |

**Precedence rule.** Three sources, in descending authority: **the live API**, then the
OpenAPI schema, then the prose docs. Where they disagree this document records all of
them and the SDK follows the highest. Every known disagreement is listed in §8.

This ordering is not academic. The prose is wrong about the Score minimum, the schema is
silent on the Score maximum, and *neither* mentions the `400` status or the object form
of `detail` — all of which the live API uses. Anything here marked "undocumented" was
found by sending a request and reading the answer.

---

## 1. Endpoints

```http
POST https://api.typesafe.ai/v1/systemone
GET  https://api.typesafe.ai/v1/models

Authorization: Bearer <API_KEY>        # securitySchemes: HTTPBearer
Content-Type: application/json
```

The spec declares no `servers` block, so the base URL comes from the documentation and
from the official SDKs' `DEFAULT_BASE_URL`.

## 2. Request body — `SystemOneRequest`

Required: **all three**.

| Field | Type | Notes |
|---|---|---|
| `state` | `string \| object \| array` | The content every question refers to. |
| `model` | `string` | Name or alias. See §6. |
| `questions` | `map<string, Question>` | `minProperties: 1`. Keys are caller-chosen. |

Question ids are chosen by the caller and returned unchanged as the answer keys. The
documentation states they are **not sent to the model** and play no part in inference —
so they are safe to make descriptive, and useless as a place to put instructions.

```json
{
  "state": "Help! My payouts have been failing for 3 days.",
  "model": "jev-latest",
  "questions": {
    "is_urgent": { "type": "noul", "instructions": "Does this convey urgency?" }
  }
}
```

## 3. Question types

`EntryType` is the recurring union the spec uses for every human-readable field:

```
EntryType = string | object | array | null
```

It appears as `instructions`, as Choice option descriptions, as Score level
descriptions, and as `criteria.true` / `criteria.false` on a Noul. Structured values are
a supported feature, not an accident — see `docs.typesafe.ai/primitives/advanced`.

### 3.1 Noul — `required: ["type"]`

| Field | Type | Required |
|---|---|:---:|
| `type` | `"noul"` | ✅ |
| `instructions` | `EntryType` | — |
| `criteria` | `NoulCriteria \| null` | — |

`NoulCriteria` has two optional `EntryType` members, `true` and `false`, describing what
a value near 1 and near 0 mean.

> Both `instructions` and `criteria` are optional and nullable. A Noul with neither says
> nothing about what to judge; the SDK warns but does not reject (§7).

### 3.2 Choice — `required: ["type", "criteria"]`

| Field | Type | Required |
|---|---|:---:|
| `type` | `"choice"` | ✅ |
| `instructions` | `EntryType` | — |
| `criteria` | `map<string, EntryType>` | ✅ |

Keys are the options. A `null` value means "interpret this option by its name alone" and
**must be sent as JSON `null`** — not `""`, not an omitted key.

### 3.3 Score — `required: ["type", "criteria"]`

| Field | Type | Required |
|---|---|:---:|
| `type` | `"score"` | ✅ |
| `instructions` | `EntryType` | — |
| `criteria` | `array<EntryType>`, `minItems: 1` | ✅ |

**Ordered.** A description's position is its score, starting at zero.

| Bound | Value | Source |
|---|---|---|
| Minimum levels | **1** | Schema `minItems: 1`. Confirmed live: a one-level Score returns `200` with `score: 0.0`, `confidence: 1.0`. The prose docs' "at least two levels" is **wrong**. |
| Maximum levels | **10** | **Undocumented in both the schema and the prose.** Eleven levels returns `400`: `Too many score levels. Must have at most 10 levels.` |

Both bounds were established empirically; neither is discoverable from any published
source. A one-level Score is legal but degenerate — the distribution has nowhere to go.

## 4. Response body — `SystemOneResponse`

Required: `model`, `answers`, `usage`.

```json
{
  "model": "jev-1.13.0",
  "answers": { "is_urgent": { "type": "noul", "noul": 0.92 } },
  "usage": { "input_tokens": 312, "output_tokens": 48 }
}
```

`model` reports the **versioned id that answered**, which may differ from the alias sent.
Log it: it is the only way to know which weights produced a given result, and aliases
move without notice.

`usage.input_tokens` / `output_tokens` are integers. Only input tokens are billed.

## 5. Answer types

`Answer` is a `oneOf` discriminated on `type`.

### 5.1 Noul — `required: ["type", "noul"]`

| Field | Type |
|---|---|
| `type` | `"noul"` |
| `noul` | `number` — P(yes), 0 to 1 |

**There is no `confidence` field, and adding one is a bug.** The probability *is* the
uncertainty: near 0.5 is the model saying it does not know. Code that reads a confidence
off a Noul reads a zero that means nothing. Guarded by `TestNoulAnswerHasNoConfidence`.

### 5.2 Choice — `required: ["type", "choice", "probabilities", "confidence"]`

| Field | Type |
|---|---|
| `choice` | `string` — the highest-probability option |
| `probabilities` | `map<string, number>` — every option, summing to 1 |
| `confidence` | `number`, 0 to 1, derived from the distribution |

### 5.3 Score — `required: ["type", "score", "legend", "probabilities", "confidence"]`

| Field | Type |
|---|---|
| `score` | `number` — probability-weighted mean of level indices; may fall between levels |
| `legend` | `map<string, EntryType>` — level index → its description |
| `probabilities` | `map<string, number>` — level index → probability, summing to 1 |
| `confidence` | `number`, 0 to 1 |

```json
{
  "type": "score",
  "score": 1.6,
  "legend":        { "0": "Calm", "1": "Frustrated", "2": "Very angry" },
  "probabilities": { "0": 0.05,   "1": 0.3,          "2": 0.65 },
  "confidence": 0.78
}
```

> ### The numeric-key question, corrected
>
> `legend` and `probabilities` keys are strings holding integers. Sorting them as
> strings would put `"10"` between `"1"` and `"2"`.
>
> **This is currently unreachable.** The server caps a Score at ten levels, so indices
> never leave `0..9`, where string order and numeric order coincide. An earlier
> revision of this document called it a live bug and claimed several community Go
> SDKs were broken by it. That was wrong, and it was wrong because the claim was made
> from the schema without testing the boundary.
>
> The SDK still parses keys as integers rather than sorting them as strings. That is
> cheap insurance against the cap being raised, not a defect being fixed.
> `TestScoreLevelKeysAreContiguousIntegers` asserts the invariant that actually holds:
> keys are the contiguous integers `0..n-1`.

> ### ⚠ `legend` values are `EntryType`, not `string`
>
> The prose documentation types `legend` as `map<string, string>`. The schema types its
> values as the same `EntryType` union used everywhere else — because a Score level
> description may itself be an object or array. Modelling `legend` as
> `map[string]string` silently breaks for structured rubrics. Fixture
> `06_structured_criteria` covers this.

## 6. Models

`GET /v1/models` → `{ "models": [ { "name", "description", "release_date" } ] }`,
all three fields required.

Returns aliases. Versioned ids such as `jev-1.13.0` are accepted by the `model` field
whether or not they are listed.

| Alias | Resolves to |
|---|---|
| `jev-latest` | `jev-1.13.0` — SDK default, matching the official SDKs |
| `jev-preview` | `jev-1.13.0` — moves ahead when a preview build exists |

### Jev 1.13 limits

| | |
|---|---|
| Price | $0.042 / Mtok input. Output free. |
| Rate limits | 250,000 tokens/sec · 1,200 requests/min |
| Context | **64k** total (state + all questions) · **32k** (state + longest single question) |
| Input | Text only — no image, audio, or video |
| Latency | ~70–500ms end-to-end |

Both context limits are enforced server-side and are separate; a request can pass the
64k check and fail the 32k one. TypeSafe states rate limits change without notice, so
the SDK treats all of these as configurable defaults, never constants.

## 7. Errors

The OpenAPI spec declares only `200` and `422` per path. The prose adds `401`, `429`,
and `529`. **The live API also returns `400`, which no published source mentions.**
Treat the documented set as a floor, never as exhaustive.

| Status | Meaning | Retry? | Documented? |
|---|---|:---:|:---:|
| `400` | Well-formed but rejected — unknown model, too many Score levels | no | **no** |
| `401` | Missing or invalid API key | no | prose |
| `422` | Body failed schema validation | **no** | spec + prose |
| `429` | Rate limit exceeded | yes, with backoff | prose |
| `529` | Overloaded | yes, with backoff | prose |
| `5xx` | Server error | yes, with backoff | — |
| `408` | Request timeout | yes (official SDKs do) | — |

### 7.1 `detail` has two shapes

This is the part most likely to be handled wrong, because only one shape is published.

**Array form** — field-level schema violations, status `422`, matching the spec's
`HTTPValidationError`:

```json
{"detail":[{"type":"missing","loc":["body","state"],"msg":"Field required","input":{...}}]}
```

**Object form** — everything else, statuses `400` and `401`, **entirely undocumented**:

```json
{"detail":{"error_type":"authentication_error","message":"Cannot authenticate with the server. Please check your API key and try again."}}
{"detail":{"error_type":"api_usage_error","message":"Unknown model: jev-does-not-exist"}}
```

**Bare-string form** — also observed, for the Score-level ceiling:

```json
{"detail":"Too many score levels. Must have at most 10 levels."}
```

A client that unmarshals `detail` into `[]ValidationDetail` gets an empty slice for the
last two and loses the server's explanation entirely. The SDK models all three:
`APIError.Detail` for the array, `APIError.Reason` for the object and bare string.

Observed `error_type` values: `authentication_error`, `api_usage_error`. The set is not
published — treat it as open.

### 7.2 Captured 422 examples

| Trigger | `loc` | `type` |
|---|---|---|
| Missing `state` | `["body","state"]` | `missing` |
| Choice without `criteria` | `["body","questions","q","choice","criteria"]` | `missing` |
| Empty `questions` | `["body","questions"]` | `too_short` |

Note the discriminator segment (`"choice"`) inserted into the path for a tagged question
type — the path is not simply the request's own key path.

`ctx` carries structured context when present, e.g.
`{"field_type":"Dictionary","min_length":1,"actual_length":0}`.

> **`input` echoes your request.** A 422 body includes the offending input, which for a
> missing-`state` error is the rest of your request. If your state contains personal
> data, it comes back in the error body and will land in any log that prints
> `APIError.Body`. The SDK keeps `Body` verbatim — it is your data — but never includes
> it in `Error()` beyond a truncated, credential-scrubbed prefix.

### 7.3 Headers

| Header | Notes |
|---|---|
| `x-typesafe-request-id` | **Confirmed present** on success and on every error. Format: `req_` + 32 hex characters. Surface it on every error. |
| `retry-after` | Honored by the official SDKs on 429/529. **Not yet observed** — triggering a real rate limit was out of scope. The SDK handles both its presence and its absence. |

## 8. Known discrepancies

Recorded rather than resolved. Each is settled by `TestObserveErrorEnvelopes` once an
API key is available.

### 8.1 Score level bounds — **resolved empirically**

| Source | Minimum | Maximum |
|---|---|---|
| OpenAPI schema | `minItems: 1` | not stated |
| Prose docs | "at least two levels" | not stated |
| **Live API** | **1 (accepted, `200`)** | **10 (eleven returns `400`)** |

The schema is right about the minimum and the prose is wrong. Neither mentions the
maximum. **SDK behavior:** accept 1–10 levels; warn on 1 (degenerate); reject >10
client-side with a clear message rather than spending a round trip to learn it.

### 8.2 `legend` value type — **resolved in favor of the schema**

Prose says `map<string, string>`; schema says `map<string, EntryType>`. The schema is
strictly wider and is consistent with Score levels accepting structured descriptions
everywhere else. The SDK models `EntryType`. See §5.3.

### 8.3 Declared error statuses — **both sources incomplete**

The spec lists only `200`/`422`; the prose adds `401`, `429`, `529`. Neither mentions
`400`, which the live API returns for an unknown model and for too many Score levels.
Neither documents the object form of `detail`. Handle the union, and assume it will
grow.

### 8.4 `instructions` optionality — **resolved in favor of the schema**

The prose presents `instructions` as the question. The schema marks it optional and
nullable on all three types. A Choice or Score can therefore be driven by `criteria`
alone. The SDK permits it and warns.

## 9. Golden fixtures

`testdata/contract/`, every file validated against `testdata/spec/openapi.json`.

| Fixture | Covers |
|---|---|
| `01_noul_single` | Minimal Noul with true/false criteria |
| `02_choice_single` | Choice with string descriptions |
| `03_score_single` | Three-level Score |
| `04_mixed_three` | All three types in one call |
| `05_structured_instructions` | Object-valued `instructions` |
| `06_structured_criteria` | Object-valued Choice descriptions and Score levels; structured `legend` |
| `07_null_criteria` | `null` option descriptions, and a `null` on `criteria.false` |
| `08_nested_state` | Nested object state with arrays; backtick state-path references |
| `09_score_max_levels` | 10 levels — the server maximum |
| `10_score_single_level` | 1 level — the true minimum; captured from the live API |

**Provenance.** Every request validates against the live schema, and **all ten have been
replayed against the live API and returned a shape matching their fixture** (2026-09-18,
`TestContractDrift`). The contract reading in this document is therefore verified, not
inferred.

Response fixtures are still *constructed* rather than captured, except
`10_score_single_level`, which is a real captured payload. They are internally consistent
— distributions sum to 1, scores equal their probability-weighted means, legends match
their criteria — and the live replay confirms their structure. But the specific
probabilities are invented.

Do not cite a fixture's numbers as evidence of model behavior. Its *shape* is verified;
its *values* are illustrative.
