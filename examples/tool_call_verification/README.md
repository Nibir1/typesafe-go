# tool_call_verification

Before an agent acts, check that the action matches what the user asked for.

```bash
go run ./tool_call_verification
```

```
1. cancel_order(map[order_id:4471 reason:wrong_size])
   matches 0.98  broader 0.04  blast 1.40
   -> execute

2. cancel_order(map[order_id:* reason:wrong_size])
   matches 0.58  broader 0.09  blast 1.41
   -> confirm with the user (needed 0.78, got 0.58)

3. refund_order(map[amount:240 order_id:4471])
   matches 0.02  broader 0.87  blast 2.07
   -> refuse: wider than the request
```

## What to notice

**The tool name is not the signal.** Proposals 1 and 2 are both `cancel_order`
and both are well-formed. The difference is `order_id: "*"`, and it is only
wrong *relative to what the user said*. A schema validator passes both.

**The model did not read `"*"` as a wildcard.** This is the finding worth taking
away, and it is a limitation, not a success. Proposal 2 cancels *every* order,
and `is_broader` scored it **0.09** — essentially "no, this is not wider than
asked". What caught it was `matches_request` falling to 0.58, which pushed it
into the confirm band. The safety margin held, but not for the reason the
question was written for.

Jev's jaggedness notes cover this: symbolic and encoded values are read as text,
not decoded into meaning. `"*"`, `-1`, `"all"` and `0xFF` are sentinels your code
understands and the model does not.

**So validate sentinels in code, not in a question.** A wildcard, an unbounded
range, a missing `WHERE` clause — check those with an `if`, and use the model
for the part that genuinely needs judgement: whether the *intent* matches.

**The gate scales with the consequence.** `blast_radius` feeds the threshold, so
a read-only call needs 0.70 and an irreversible one needs ~0.95. A single
threshold for every tool is either too strict to be useful or too loose to be
safe.

**"Wider than asked" still earns its place.** It is what caught proposal 3, where
the user asked for a status and the agent proposed a refund — scored 0.87 and
refused outright.

**This is a guardrail, not a permission system.** It reduces the rate of wrong
actions; it does not make them impossible — proposal 2 above is the proof. Anything genuinely irreversible
should still require a human, and the `blast_radius` score is a good input to
deciding which those are.

## Cost

One round trip per proposed call, at 70–500ms. For an agent that acts rarely
and consequentially, that is cheap. For one that makes hundreds of read-only
calls, gate it on `blast_radius` and skip the check for reads.

## Next

- [`llm_guardrails`](../llm_guardrails) — the same idea for text rather than actions
