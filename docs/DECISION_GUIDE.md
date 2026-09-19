# DECISION_GUIDE.md

Which primitive to reach for, and how to word the question once you have.

---

## The one-line version

| You want | Use | You get |
|---|---|---|
| Is this true? | `Noul` | A probability in [0,1] |
| Which one of these? | `Choice` | The winner, a distribution, a confidence |
| How much, on a scale I define? | `Score` | A weighted position, a distribution, a confidence |

---

## `Noul`

**Use it when the answer is a proposition that is either true or not.**

```go
typesafe.Noul{
    Instructions: "Does this message convey urgency?",
    Criteria: &typesafe.NoulCriteria{
        True:  "The sender needs a response today",
        False: "The sender can wait",
    },
}
```

The answer is `0.93`, not `true`. **The threshold is yours** — it is the point
where you would rather be wrong one way than the other, and that is a product
decision the model has no view on.

**A `Noul` has no confidence field, and this is not an omission.** The
probability *is* the uncertainty. `0.5` means genuinely undecided. Asking for a
confidence alongside it would be asking how sure the model is about how sure it
is.

### Wording

**Write `True` and `False` as presences, not absences.** TypeSafe documents that
a `Noul` whose `true` describes an absence performs measurably worse, and
nothing in the returned probability reveals the mistake. `False: "A genuine
message from a real correspondent"` beats `False: "Not spam"`.

**One proposition per question.** "Is this spam and abusive?" returns a single
number covering both, and no threshold recovers the parts. The
`atomicquestion` analyzer flags this.

---

## `Choice`

**Use it when exactly one of a known set applies.**

```go
typesafe.Choice{
    Instructions: "Which team should handle this ticket?",
    Criteria: typesafe.Options{
        "billing":   "Payments, invoicing, refunds",
        "technical": "Bugs, outages, integrations",
        "other":     nil,   // read by its name alone
    },
}
```

The answer names the winner and gives a probability for every option, so the
result is always a member of the set you supplied.

### Include a catch-all — or deliberately do not

This is the decision people make by accident. Measured, on the same input:

| Options | Answer | Confidence |
|---|---|---|
| with `unclear` | `unclear` | **1.00** |
| without `unclear` | `technical_help` | **0.47** |

**A catch-all absorbs ambiguity and raises confidence.** Without one the model
must pick from what it was given, and a poor fit shows up as a low-confidence
guess instead.

Include one when "none of these" is an outcome you can act on. Leave it out
when you would rather see ambiguity as low confidence. Both are defensible;
choosing without knowing the effect is not.

### `Confidence` and `Margin` answer different questions

`Confidence` comes from the shape of the whole distribution. `Margin` is the gap
to the runner-up. A three-way near-tie and a clear winner with a long tail can
have similar confidence and very different margins — print both while you are
tuning.

---

## `Score`

**Use it when the answers are ordered.**

```go
typesafe.Score{
    Instructions: "How severe is the problem described here?",
    Criteria: typesafe.Levels{
        "No impact on the customer",
        "Annoying but there is a workaround",
        "One workflow is blocked",
        "The product is unusable",
    },
}
```

Order is meaning: a level's position is its score, lowest first. One to ten
levels — the API rejects eleven with a `400` that no published source mentions.

### Do not read a magnitude out of a score

`severity.Score` comes back as `2.19`. It is tempting to compare that against
`2.5`. **Don't.** TypeSafe documents that Jev's score levels are weakly
calibrated numerically: the ordering is reliable, the spacing is not.

```go
// sound — works on the distribution the model actually produced
if answer.AtOrAbove(2) > 0.8 { escalate() }

// not sound — treats an ordinal position as a measured quantity
if answer.Score > 2.5 { escalate() }
```

If you need a real number, extract it as a `Choice` over enumerated ranges and
compute in code.

### `Nearest` and `MostLikely` can disagree

When they do, the distribution is telling you the question has more than one
reading. A bimodal answer — heavy at both ends, light in the middle — has a
weighted mean sitting in a level the model considers unlikely.
`ScoreIsBimodal` detects it.

---

## Choosing between them

**Ordered? Use `Score`.** "How severe", "how urgent", "how complete". Encoding
an ordered rubric as a `Choice` throws away the ordering, and then
`AtOrAbove` has nothing to work with.

**Unordered and exclusive? Use `Choice`.** Which team, which intent, which
category.

**Independent propositions? Use several `Noul`s, not one `Choice`.** If a ticket
can be *both* a billing question *and* a bug report, a `Choice` forces the model
to pick one. Two `Noul`s let both be true.

**Binary but you want a confidence?** You want a `Noul`, and what you want is
its probability. Do not model a yes/no as a two-option `Choice` to get a
confidence field — you will get a confidence about a distribution over two
options, which is a less direct measure of the same thing.

---

## Composing several answers

Ask everything about one state in a single request. Questions in one call are
evaluated in parallel and cost only their own tokens; a second *state* costs a
whole request.

Then combine in code, where the logic is reviewable:

```go
risk := decision.Policy{
    Weights:     decision.Weights{"is_solicitation": 3, "is_hostile": 2},
    Normalize:   true,
    BlockAbove:  0.80,
    OnMissing:   decision.MissingIsError,
}
result, err := risk.Evaluate(resp)
```

`result.Trace` is the arithmetic. A verdict without it is an assertion, and an
appeals process needs the arithmetic.

**Do not guess the weights twice.** `decision.Calibrate` fits them from labelled
examples and reports whether your questions separate your labels at all. If
`Separable` is false, fix the questions — no weighting rescues signals that do
not discriminate.

---

## Questions to avoid entirely

From TypeSafe's published jaggedness notes, each enforced by the `jaggededge`
analyzer:

| Don't ask | Why | Instead |
|---|---|---|
| "How many X are there?" | Recognizes the shape of an answer rather than tallying; error grows with the count | One `Noul` per item, sum in code |
| "Which date came first?" | Dates are read as text, not ordered quantities | Parse and compare in code |
| "What is 15% of this?" | Arithmetic is unreliable | Extract the number, compute in code |
| "Is this not un-clear?" | Double negatives degrade measurably | Rewrite as a presence |
| "Summarize this" | Not what System One is for | Use a generative model |
| Hex, RGB, base64 values | Encodings are read as text | Decode in code first |

The same applies to sentinels. `order_id: "*"` meaning *every order* was not
recognized as broader in scope in [the tool-call
example](../examples/tool_call_verification) — `"*"` is a convention your code
knows and the model does not. Validate those with an `if`.

---

## A checklist

Before you send a question set:

- [ ] Each question asks exactly one thing
- [ ] `Noul` criteria are written as presences
- [ ] A `Choice` has a catch-all, or deliberately does not
- [ ] `Score` levels are in order, lowest first, and at most ten
- [ ] Nothing asks the model to count, calculate, compare dates, or decode
- [ ] Every question about this state is in *this* request
- [ ] `EstimateTokens` is under the limits
- [ ] The code reads `Confidence`, not just the winning option

`typesafe lint -f request.json` checks most of it, and the analyzers check the
rest at build time.

---

The [user manual](MANUAL.md) puts this in context; [LIMITS.md](LIMITS.md) covers
what the model is weak at, and [examples/](../examples) shows each pattern running.
