# confidence_routing

Act, check, or refuse — and where low confidence actually comes from.

```bash
go run ./confidence_routing
```

```
"I want a refund for the duplicate charge on my card."
  with_catchall     refund            conf 1.00  margin 1.00  -> act
  without_catchall  refund            conf 1.00  margin 1.00  -> act
  -> executing refund

"hey"
  with_catchall     unclear           conf 1.00  margin 1.00  -> act
  without_catchall  technical_help    conf 0.47  margin 0.21  -> escalate
  -> asking the customer to say more

"it says error and also I think I paid twice but not sure, can you look"
  with_catchall     technical_help    conf 0.38  margin 0.11  -> escalate
  without_catchall  technical_help    conf 0.34  margin 0.06  -> escalate
  -> routing to a person; the model is not sure enough
```

## What to notice

**Vagueness does not lower confidence. Ambiguity does.**

This is the finding the example was rewritten around, and it is not what you
would guess. `"hey"` is as vague as an input gets, and the model answers
`unclear` with confidence **1.00** — because `unclear` is genuinely the right
option, and the model is sure of it. Confidence falls when two options both
*fit*, as in the third message, which is about a bug and a double charge at
once.

So low confidence means **your options overlap for this input**, not that the
input was poor. That is a signal about your question design, not just about the
message.

**A catch-all option absorbs ambiguity — and inflates confidence.** Look at
`"hey"` again. With `unclear` available: a correct answer at 1.00. Without it,
the model must pick from what it was given, and returns `technical_help` at
0.47 — a guess, correctly flagged as one by the low confidence.

Include a catch-all when "none of these" is a real outcome you can act on.
Leave it out when you want ambiguity to surface as low confidence instead.
Either is defensible; picking without knowing the effect is not.

**A confident `unclear` is not an escalation.** It is a correct answer that
must not be dispatched as an intent. Ask the customer to say more — do not
route it to a colleague who knows no more than you do. Branching on the band
alone collapses these two.

**The middle band is the one people forget.** "Act, but verify" costs almost
nothing and removes most of the risk of automation. Without it you are choosing
between acting on 0.62 and escalating everything below 0.9.

**Jev is confident.** Across these inputs the only sub-0.5 answers came from
genuine ambiguity. Do not tune `ConfirmAbove` from intuition — measure it on
your own traffic, or the middle band will never fire.

## A note on reproducibility

Re-recording this cassette does not always produce identical numbers. The third
message scored 0.75 on one run and 0.38 on another with the same question set.
Jev is documented as highly consistent but is **not** contractually
deterministic, which is the same property that makes
[`typesafecache`](../../typesafecache) key on the resolved model id rather than
on an alias.

Do not build a test that asserts an exact probability.

## Next

- [`intent_routing`](../intent_routing) — dispatching to handlers with a typed enum
- [`llm_guardrails`](../llm_guardrails) — the same pattern guarding a generative model
