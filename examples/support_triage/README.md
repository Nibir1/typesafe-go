# support_triage

All three primitives, one call, one ticket.

```bash
go run ./support_triage
```

```
team:     billing    confidence 0.90 (margin 0.86)
severity: 2.19      confidence 0.78
repeat:   0.98

P(at least blocking) = 0.98
-> page the on-call for billing
```

## What to notice

**One call, three questions.** Questions about the same state are evaluated in
parallel server-side and cost only their own tokens. A second *state* costs a
whole request, and the state is by far the larger part of the bill. Pack
questions in; fan out over states.

**Threshold the distribution, not the score.** `severity.Score` is `2.41`, which
looks like a number you could compare against `2.5`. Do not. TypeSafe documents
that score levels are weakly calibrated numerically — "is this at least
Blocking?" is sound, "is this 2.4?" is not. `AtOrAbove(2)` asks the sound
question, and it works on the distribution the model actually produced.

**`Margin` and `Confidence` answer different questions.** Confidence comes from
the shape of the whole distribution; margin is the gap to the runner-up. A
three-way near-tie and a clear winner with a long tail can have similar
confidence and very different margins.

**The catch-all option has a `nil` description.** It is sent as JSON `null`,
which the API reads as "interpret this by its name alone". Without a catch-all
the model must pick from what it was given, however badly it fits.

## Next

- [`composite_scoring`](../composite_scoring) — combining these signals with weights
- [`batch_feature_extraction`](../batch_feature_extraction) — the same questions over thousands of tickets
