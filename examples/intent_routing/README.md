# intent_routing

A `Choice` over a Go enum, dispatched to handlers the compiler checks.

```bash
go run ./intent_routing
```

```
I was charged twice for January, please refund -> refund           1.00 act
     opening a refund case
How do I add a second seat to my plan?         -> billing_question 1.00 act
     answering from the billing FAQ
it's broken again                              -> technical        1.00 act
     creating an engineering ticket
```

## What to notice

**The enum is the single source of truth.** `TypedChoice[Intent]` builds the
question from the same constants the `switch` dispatches on, so the option set
and the handlers cannot drift apart. Write `case "refund":` with a typo in an
untyped version and it compiles, never matches, and falls through silently.

**`Exhaustive` catches the drift types cannot.** The compiler checks the arms
you wrote; it cannot check what the *server* sends back. An answer naming an
option the question never declared means the request and the enum have come
apart — which a type switch handles by matching no branch at all.

**Confidence gates the dispatch, not just the choice** — but notice that all
three messages here score 1.00, including the terse `it's broken again`. Jev is
confident when the options do not overlap, and these four intents are well
separated.

Do not read that as "the confidence check is unnecessary". It means the check
costs nothing on traffic like this and earns its place on traffic that is
genuinely ambiguous. [`confidence_routing`](../confidence_routing) shows what
that looks like, and where low confidence actually comes from.

**The tests assert the invariants, not the answers.** `TestEveryIntentHasAHandler`
and `TestQuestionOffersEveryIntent` fail when someone adds a constant and
forgets the rest. Those are the failures that reach production; the model's
answer for one fixed message is not.

## Next

- [`composite_scoring`](../composite_scoring) — when one question is not enough to decide
