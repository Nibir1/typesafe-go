# composite_scoring

Four atomic signals, weights in code, and a verdict you can defend.

```bash
go run ./composite_scoring
```

```
community_moderation.v3: block (score 0.9037)

contributions, largest first:
  is_solicitation    3.0 x 0.970 = 2.910
  is_unverifiable    2.0 x 0.980 = 1.960
  is_hostile         2.0 x 0.720 = 1.440
  is_urgent_push     1.0 x 0.920 = 0.920

-> remove the post and notify the author
```

## What to notice

**One broad question cannot be split back apart.** Asking "should this be
removed?" returns a single probability covering solicitation, unverifiable
claims, hostility and urgency all at once. No threshold recovers the parts, and
you cannot tell an author *why*. The `atomicquestion` analyzer flags compound
questions for exactly this reason.

**The weights are code.** They are reviewable, versionable, diffable and
testable, and changing one is a pull request rather than a prompt edit nobody
can see. The policy name carries a version for the same reason: when a verdict
is appealed six months later, you need to know which weights produced it.

**`Normalize` keeps thresholds stable.** The score stays in `[0,1]`, so adding
a fifth signal later does not silently shift every threshold you tuned.

**`MissingIsError` is the right default.** If a weighted question has no answer —
the request and the policy drifted apart — scoring it as zero quietly lowers
every verdict. Failing is loud, and loud is correct here.

**The trace is the deliverable.** A verdict on its own is an assertion. The
contributions are the arithmetic, identical for every post, and they are what
an appeals process actually needs.

## Calibrating the weights

Do not guess them twice. `decision.Calibrate` fits weights from labelled
examples and reports whether the signals separate the classes at all:

```go
report, err := decision.Calibrate(samples)
if !report.Separable {
    // the questions do not distinguish your labels; fix the questions,
    // not the weights
}
```

## Next

- [`llm_guardrails`](../llm_guardrails) — the same machinery guarding a generative model
