# batch_feature_extraction

The same questions over a corpus, with bounded concurrency and per-item error
isolation.

```bash
go run ./batch_feature_extraction
```

```
#  sentiment  ship  support  defect  review
1  1.19       0.99  0.01     0.05    Shipping took three weeks and the box
2  4.00       0.01  0.01     0.02    Absolutely brilliant. Third one I've b
3  1.91       0.03  0.03     0.05    Does what it says. Nothing special.
4  0.03       0.02  0.99     0.96    Stopped working after two days and sup
5  1.44       0.01  0.02     0.08    Good value but the instructions are tr

5 succeeded, 0 failed, 1854 input tokens, 412ms
```

## What to notice

**It batches states, never questions.** Jev ingests the state once and evaluates
every question against it in parallel, so a fifth question costs only its own
tokens while a sixth review costs a whole request. Pack questions in; fan out
over states.

**Results come back in input order.** `result.Items[i]` belongs to
`reviews[i]`, whatever order the responses arrived in — so joining back to your
database rows is an index, not a correlation.

**One failure does not abort the batch.** Each item carries its own error and
the rest run at full speed. `result.Err()` is a *grouped summary*, not the first
failure: a thousand items failing for two different reasons reads as two counted
lines.

**Concurrency adapts on its own.** When any worker meets a 429, the limit for the
whole batch halves and then recovers. Without that, every worker independently
rediscovers the same rate limit while the batch keeps pushing at the rate that
caused it.

## For a corpus that does not fit in memory

Range over the streaming view instead. Results arrive in completion order, and
`ItemResult.Index` gives the input position:

```go
for i, item := range client.SystemOneBatchSeq(ctx, states, questions) {
    if item.Err != nil {
        continue
    }
    writeRow(i, item.Response)
}
```

Breaking out of the loop cancels the batch and leaves nothing running.

## Cost control

Extracting features from a large corpus is where the bill shows up. Two things
help:

- `typesafe.WithBudget` caps spend per process — `MaxTotalTokens` is a hard stop.
- `typesafecache` skips re-evaluating states you have already seen, which matters
  for a corpus that grows by append.

## Next

- [`support_triage`](../support_triage) — the question set this scales up
