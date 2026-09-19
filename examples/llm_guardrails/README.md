# llm_guardrails

Check a generative model's draft before a user ever sees it.

```bash
go run ./llm_guardrails
```

```
draft 1
  grounded    0.87
  answers     0.98
  overpromise 0.12
  tone        1.00 (Neutral and factual)
  verdict     allow (risk 0.126)
  -> send

draft 2
  grounded    0.01
  answers     0.73
  overpromise 0.98
  tone        2.94 (Overfamiliar or salesy)
  verdict     block (risk 0.986)
  -> do not send; regenerate
```

## What to notice

**Both drafts are fluent.** The second one is confident, friendly, well-written
and completely made up. Fluency is not a signal, which is exactly why asking a
generative model to check its own work does not work — you get another fluent
answer.

**The state is structured, not flattened.** `question`, `source` and `draft` are
named fields, and the questions refer to them by name. "Is this grounded?" is
meaningless without the source in the same state; a flattened paragraph makes
the model guess which part is which.

**Risk is scored on the inverted signal.** The policy weighs `not_grounded`,
not `grounded`, because it is measuring risk. The *question* stays written as a
presence — "is every claim supported?" — because that is how a `Noul` performs
best, and `decision.Not` does the inversion in code where it is visible.

**Off-topic is a separate outcome from wrong.** A draft can be perfectly
grounded and still not answer the question. That is a regenerate, not a block,
and collapsing the two loses the distinction that tells you which to do.

## Where to put this

Run it on the draft, not on the prompt. Guarding the input tells you what the
user asked for; guarding the output tells you what you are about to say.

At 70–500ms this adds a round trip to every generation. The alternative is a
human reading everything, or nobody reading anything.

## Next

- [`tool_call_verification`](../tool_call_verification) — the same idea for actions rather than text
