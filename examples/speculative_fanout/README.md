# speculative_fanout

Ask everything at once and throw away what you do not need.

```bash
go run ./speculative_fanout
```

```
financial:       0.99
type:            invoice (confidence 1.00)
late penalty:    0.98
bank details:    0.89
-> contains payment details; route to the restricted queue

cost: 484 input tokens, one round trip
```

## What to notice

**The obvious shape is a chain, and it is the wrong one.** Ask "is it
financial?", then if yes "is it an invoice?", then if yes "what are the terms?"
— three round trips of 70–500ms each, one after another, because each depends
on the last.

**Questions in one request are evaluated in parallel and cost only their own
tokens.** The state is sent once and is the expensive part. So ask every
question you *might* need up front, branch locally, and discard the answers the
branch made irrelevant.

The trade is a few hundred extra tokens against two saved round trips. At the
documented 70–500ms per call, that is not close.

**It only works within one state.** A question about a *different* document is a
different request, and no amount of fanning out avoids that. See
[`batch_feature_extraction`](../batch_feature_extraction) for the other axis.

**Do not fan out past the context budget.** Every question adds to the same
request, and the API rejects a request over 64,000 tokens. `EstimateTokens`
tells you before you send.

## Next

- [`composite_scoring`](../composite_scoring) — turning several signals into one decision
