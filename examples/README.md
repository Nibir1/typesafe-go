# Examples

Ten runnable programs, each one a pattern rather than a demo. Every one is
covered by a test that replays a cassette recorded from the live API, so they
run offline, in CI, with no key.

```bash
go run ./quickstart          # needs TYPESAFE_API_KEY
go test ./...                # offline, replays the cassettes
go test ./... -update        # re-record against the live API
```

| | |
|---|---|
| [quickstart](quickstart) | One yes/no question, and a threshold you own |
| [support_triage](support_triage) | All three primitives in one call |
| [spam_detection](spam_detection) | Two thresholds, three actions |
| [confidence_routing](confidence_routing) | Act, check, refuse — and where low confidence comes from |
| [speculative_fanout](speculative_fanout) | Ask everything at once, branch locally |
| [composite_scoring](composite_scoring) | Weighted signals with an auditable trace |
| [intent_routing](intent_routing) | A typed enum the compiler checks both ends of |
| [llm_guardrails](llm_guardrails) | Check a generative model's draft before sending |
| [tool_call_verification](tool_call_verification) | Check an agent's action against what was asked |
| [batch_feature_extraction](batch_feature_extraction) | The same questions over a corpus |

## Suggested order

Start with **quickstart**, then **support_triage** for the shape of a real
request. After that:

- Deciding what to do with an answer — **confidence_routing**, **spam_detection**
- Combining several answers — **composite_scoring**, **speculative_fanout**
- Type safety — **intent_routing**
- Guarding other models — **llm_guardrails**, **tool_call_verification**
- Scale — **batch_feature_extraction**

## Three things the examples found

These are not hypotheticals. Each was discovered by running the code and
finding the output disagreed with what had been written about it.

**Vagueness does not lower confidence; ambiguity does.** `"hey"` scores 1.00 for
`unclear`, because that is the right answer. Confidence falls when two options
both fit. See [confidence_routing](confidence_routing).

**A catch-all option inflates confidence.** The same input scores 1.00 with an
`unclear` option and 0.47 without it. The option set changes the answer as much
as the input does.

**The model does not decode sentinels.** `order_id: "*"` — a wildcard meaning
*every order* — was not flagged as broader in scope. Validate sentinels with an
`if`; use the model for judgement. See
[tool_call_verification](tool_call_verification).

## Why the output in each README is exact

Every sample output is copied from an actual run against the recorded cassette,
not written by hand. Several of them replaced invented numbers that turned out
to be wrong, which is the argument for doing it this way.

Re-recording will not always reproduce them exactly: Jev is documented as
highly consistent but is not contractually deterministic. Do not write a test
that asserts an exact probability.
