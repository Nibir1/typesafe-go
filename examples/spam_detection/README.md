# spam_detection

Two thresholds, three actions, and the band in between where a person is
cheaper than either mistake.

```bash
go run ./spam_detection
```

```
p(spam)  action      message
0.98     block       WINNER!!! Claim your FREE iPhone now: bit.ly
0.04     deliver     Hi Sarah, following up on the invoice we dis
0.89     quarantine  Quick question about your extended car warra
```

## What to notice

**One threshold throws away the most useful part of the answer.** A classifier
that returns `true`/`false` forces you to pick a single cut point, and every
message near it is a coin flip you have to live with. Two cut points give you a
third option — quarantine — which is where the ambiguous cases belong.

**Pick the thresholds from the cost of being wrong.** Deleting a real invoice is
far worse than quarantining a scam, so `blockAbove` is high and `flagAbove` is
low. Those numbers are a product decision; the model has no view on them.

**Write `Criteria` as presences, not absences.** `False` here is "a genuine
message from a real correspondent", not "not spam". TypeSafe's jaggedness notes
call out double negatives specifically, and the `jaggededge` analyzer will flag
them in your own code.

**One request per message.** These are different *states*, so they cannot share
a call. For thousands of them, see
[`batch_feature_extraction`](../batch_feature_extraction).

## Next

- [`confidence_routing`](../confidence_routing) — the same idea with `Choice` and its confidence
