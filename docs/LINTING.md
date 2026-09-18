# LINTING.md

Three static analyzers that check TypeSafe questions at build time.

They live in `lint/`, a **separate Go module**, because `go/analysis` comes from
`golang.org/x/tools` and the core SDK guarantees zero third-party dependencies. Nothing
here is pulled in by importing `github.com/nibir1/typesafe-go`.

---

## Why a linter for questions

Most mistakes in a TypeSafe integration are quiet. The API answers the question you
asked, confidently, and returns nothing to indicate the question was a poor fit:

- Ask the model to count items and you get a plausible number that grows less accurate
  with the size of the list.
- Ask which of two dates comes first and you get a confident answer that is unreliable
  by design — Jev reads dates as text, not as ordered quantities.
- Ask a compound question and you get one probability covering two propositions, which
  no threshold can split apart afterwards.

None of these produce an error. There is no response field that says "that question was
unsuitable". Build time is the only place to catch them, which is what these are for.

---

## Install and run

```bash
go install github.com/nibir1/typesafe-go/lint/cmd/typesafe-lint@latest
```

Three ways to run them, all equivalent:

```bash
# Standalone
typesafe-lint ./...

# As a vet tool — no new build step in an existing pipeline
go vet -vettool=$(which typesafe-lint) ./...

# One analyzer at a time
typesafe-lint -jaggededge ./...
```

---

## `atomicquestion`

Enforces the rule TypeSafe repeats on every pattern page: decompose a broad judgment
into atomic questions, ask them together, and combine the answers in code. Questions in
one request are evaluated in parallel and cost only their own tokens, so splitting is
nearly free.

| Check | Default |
|---|---|
| Two or more `?` in one instructions string | on |
| A conjunction joining two judgments (`and does`, `as well as`, `or whether`, …) | on |
| Instructions longer than N words | 60 |
| A `Choice` with more than N options | 12 |
| A `Choice` with exactly one option | on |

```
Noul instructions join two judgments with "and does"; one probability cannot be
split back into its halves. Ask two questions — they are evaluated in parallel
and cost only the extra tokens
```

**Flags:** `-max-words`, `-max-options`, `-include-tests`.

Bare `and` is deliberately not matched. "Terms and conditions" is one noun phrase, and
flagging it would make the analyzer unusable.

---

## `jaggededge`

Every rule here cites a section of TypeSafe's published
[Jev 1.13 jaggedness notes](https://docs.typesafe.ai/model-jaggedness/jev-1.13) and
repeats its recommended fix. That is what makes this a conformance checker rather than
an opinion — the failure modes are documented by the vendor, not guessed at.

| Check | Jaggedness section |
|---|---|
| Counting: "how many", "number of", "count the" | Math and Numbers → Counting |
| Date comparison: "earlier than", "which came first", "days between" | Date and time comparison |
| Numeric encodings: hex, RGB, binary, base64 | Math and Numbers → Numeric representations |
| Double negatives: "not un…", "never not" | Indirection |
| Generation: "write a", "summarize", "translate" | Generation |
| Arithmetic: "calculate", "what percentage" | Math and Numbers |
| A `Noul` whose `true` describes an absence and `false` a presence | Contradictory instructions and criteria |
| Instructions over 2000 characters | Large state full of irrelevant detail |

```
Noul instructions contain "how many". Jev does not count reliably — it recognizes
the shape of an answer rather than tallying, and the error grows with the size of
the thing being counted. Ask one Noul per item and sum the answers in code
(jaggedness: Math and Numbers: Counting)
```

The inverted-criteria rule is worth calling out. A `Noul` whose `true` maps to "no"
performs measurably worse, and the mistake is invisible in the result — the probability
just comes back subtly wrong.

**Flags:** `-include-tests`.

---

## `confidencecheck`

TypeSafe's confidence guidance is a three-path pattern: act automatically when
confident, proceed with a check when moderately confident, refuse to act when the model
reports it does not know. Code that branches on `answer.Choice` alone collapses all
three into one.

| Check | Why |
|---|---|
| Branching on `.Choice` / `.Score` / `.Nearest()` in a function that never reads `Confidence` | The three paths become one |
| A bare literal threshold in `.Bool(0.85)` or `.Uncertain(0.1)` | Unnamed thresholds drift apart from their siblings |
| Discarding the second result of `Confidence()` | It is the only thing distinguishing "unsure" from "a Noul, which has no confidence" |

That last one matters most. `NoulAnswer` carries no confidence — the probability *is* the
uncertainty — so `Confidence()` returns `(float64, bool)` and code that discards the
`bool` reads every Noul as maximally uncertain.

**Flags:** `-include-tests`.

### What it deliberately does not flag

The read must **drive control flow** — an `if`, `switch`, or `for` condition or init
statement. Returning or computing a value from an answer is not flagged:

```go
// flagged: this steers on an answer it has not checked
if a.Choice == "billing" { route() }

// not flagged: an accessor, not a decision
func atOrAbove(a typesafe.ScoreAnswer, n int) float64 { return a.AtOrAbove(n) }
```

Methods on the answer types are skipped — a method on `ScoreAnswer` reading `a.Score` is
the implementation, not a caller. Test files are skipped by default.

This trades recall for precision on purpose. An earlier version flagged any read and
produced 21 diagnostics against this SDK's own source, including the `decision` package's
accessors and the implementation of `Nearest` itself. **A linter that flags its own
library gets switched off, and then catches nothing at all.**

---

## Suppressing a finding

```go
//nolint:jaggededge counts are bounded at three here and verified in code
var attachments = typesafe.Noul{Instructions: "How many attachments are there?"}
```

**A suppression without a reason is itself reported.** A bare `//nolint` is an
unanswered question in the code, and why it was added is exactly what the next reader
needs.

The comment may sit on the same line as the diagnostic or on any of the four lines above
it, which covers a multi-line question literal where the `//nolint` is written above the
declaration.

---

## CI

### GitHub Actions

```yaml
- uses: actions/setup-go@v6
  with: { go-version: stable }

- name: Install the analyzers
  run: go install github.com/nibir1/typesafe-go/lint/cmd/typesafe-lint@latest

- name: Check questions
  run: go vet -vettool=$(go env GOPATH)/bin/typesafe-lint ./...
```

### Makefile

```make
analyzers:
	@go install github.com/nibir1/typesafe-go/lint/cmd/typesafe-lint@latest
	@go vet -vettool=$$(go env GOPATH)/bin/typesafe-lint ./...
```

This repository's own `make analyzers` does exactly this and **fails on any finding** —
the analyzers are held to their own standard.

### golangci-lint

The analyzers are exported for the v2 module plugin system, which links them into a
custom binary rather than loading them at runtime, so the golangci-lint version and the
analyzer version are pinned together.

`.custom-gcl.yml`:

```yaml
version: v2.13.2
plugins:
  - module: github.com/nibir1/typesafe-go/lint
    version: latest
```

Then `golangci-lint custom` builds the binary, and `.golangci.yml` enables the linters
under `linters.settings.custom`.

---

## Limitations, stated plainly

**Only constant strings are checked.** Instructions built at runtime are skipped
entirely. No static check can say anything true about them, and guessing would produce
exactly the false positives that get a linter disabled. Concatenations of literals *are*
folded, so instructions split across lines with `+` are still checked.

**Only composite literals are matched.** A question written with the fluent
constructors — `typesafe.NewChoice("…").Option(…)` — or with the typed ones —
`typesafe.TypedChoice[Topic](…)` — is **not** currently linted, and neither is anything
`typesafe-gen` emits. This hole widened in Phase 12: constructors are now the natural
way to write a question, so a growing share of them are invisible here.

Closing it means teaching `lint/internal/qast` to recognize constructor calls as well as
literals, which changes `Question.Lit` from an `*ast.CompositeLit` to a node and touches
all three analyzers. It is recorded in the roadmap as the next thing to do in this
module.

**Questions are matched by type, not by name.** A local struct you happen to call `Noul`
is not diagnosed; a question written as `ts.Noul{…}` under an import alias is. The type
checker makes that distinction, which is why these are `go/analysis` analyzers rather
than a grep.

**No automatic fixes.** Splitting a compound question needs a human decision about what
the two questions should be, and a mechanical edit would produce something that compiles
and asks the wrong thing.
