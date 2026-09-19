# quickstart

One yes/no question, and a decision made from the probability.

```bash
export TYPESAFE_API_KEY=...
go run ./quickstart
```

```
urgency: 0.93
-> escalate
```

## What to notice

**The answer is a probability, not a boolean.** `urgent.Noul` is `0.94`, and
`Bool(0.8)` is you choosing where to cut. That threshold is a product decision —
the point where you would rather be wrong one way than the other — and putting
it in your code rather than the model's is the whole idea.

**`Criteria` says what a yes and a no mean.** TypeSafe documents that a `Noul`
whose `true` describes an absence performs measurably worse, and nothing in the
returned probability reveals the mistake. Write them in the same direction as
the instructions.

**A `Noul` has no confidence field.** The probability *is* the uncertainty. 0.5
means "genuinely unsure", not "50% yes".

## Next

- [`confidence_routing`](../confidence_routing) — when 0.5 should mean "ask a person"
- [`support_triage`](../support_triage) — three questions in one call
