# Security Policy

## Reporting a vulnerability

**Do not open a public issue.**

Use [GitHub's private vulnerability reporting](https://github.com/nibir1/typesafe-go/security/advisories/new),
which goes only to the maintainers.

Include what you would want to receive: what the flaw is, how to reach it, what
an attacker gets, and a reproducer if you have one.

| | |
|---|---|
| First response | within 3 days |
| Assessment | within 7 days |
| Fix for a confirmed high-severity issue | within 30 days |

Credit is given in the advisory and the changelog unless you ask otherwise.

### Not this project

This is a **community** SDK, not TypeSafe AI's. A vulnerability in the TypeSafe
API, in Jev, or in the official SDKs should go to TypeSafe AI. Report it here
only if the flaw is in this code.

---

## Supported versions

| Version | Supported |
|---|---|
| 1.x | ✅ |
| < 1.0 | ❌ — pre-release, superseded by 1.0 |

Security fixes land on the latest minor. There is no long-term support branch;
a project maintained by volunteers should not promise one it cannot keep.

---

## What this SDK does with your data

**The API key** is read from `TYPESAFE_API_KEY` or passed to `WithAPIKey`. It is
sent as a bearer token over TLS and is never written to disk, logged, or
included in an error.

That last one is enforced rather than intended. `APIError` carries a scrubber,
and a test sends a key the server echoes back in its error body to prove the key
does not reach `Error()`. That test exists because an early version leaked it
exactly that way.

**Your state** — the content you ask questions about — is sent to the API and is
routinely personal. This SDK does not log it. `WithLogging` records the request
id, question count, duration, model and token usage, and nothing derived from
the state, because a logging helper that leaks your data by default is worse
than none.

**Cassettes record real traffic.** `cassette.WithSecret` scrubs configured
secrets on write, `cassette.Verify` checks a file, and `make secrets` scans every
`testdata` tree in the repository. Cassettes still contain real request and
response bodies — treat one like a log file, because that is what it is.

**The disk cache writes responses to a directory you choose.** Same
consideration: whatever the API returned about your data is now on disk, at
`0700`.

---

## Supply chain

**The core module has no third-party dependencies.** Not "few" — none. There is
nothing to compromise, and `make deps-graph`, a CI job, and a `depguard` lint
rule all assert it. Analyzers, tracing, metrics, caching and the integrations
live in separate modules, so you take only what you ask for.

| Control | How |
|---|---|
| Dependency graph | `go mod graph` asserted empty for the core, in CI |
| Import guard | `depguard` fails a build that imports outside the stdlib |
| Known vulnerabilities | `govulncheck` per module, on every release |
| Dependency updates | Dependabot, every module, weekly |
| Actions | pinned by commit SHA, not by tag |
| Module integrity | `go mod verify` before a release builds |
| CI builds | `GOFLAGS=-mod=readonly`, so no build edits a `go.mod` |

**Releases are signed.** Every checksum file carries a keyless Sigstore
signature: there is no private key to leak, and the certificate records which
workflow in which repository produced it.

```bash
cosign verify-blob checksums.txt \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github\.com/nibir1/typesafe-go/\.github/workflows/release\.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

**Build provenance is attested**, and an SBOM ships with each archive:

```bash
gh attestation verify typesafe_1.0.0_linux_amd64.tar.gz --repo nibir1/typesafe-go
```

### A pinned action is not a safe action

Pinning by SHA means an action cannot change under you. It does not mean the
action was safe when it was pinned. Dependabot proposes the upgrades; somebody
still has to read them.

---

## Things this SDK will not do for you

**It does not sanitize your state.** Whatever you put in `State` is sent. If it
contains secrets, they go to the API.

**It does not make an LLM's output trustworthy.** `llm_guardrails` and
`tool_call_verification` reduce the rate of bad outcomes. They are guardrails,
not permission systems, and the examples say so — the tool-call example ships
with a case the model *misses*, kept deliberately.

**It does not protect against prompt injection in the state.** A `Noul` asking
"is this abusive?" about attacker-controlled text is reading attacker-controlled
text. System One returns a probability rather than following instructions, which
helps a great deal, but do not treat a model's judgement of hostile input as a
security boundary.

**Model answers are not authorization.** An `evaluate_policy` verdict is advice
your code acts on. Something irreversible deserves a check your code makes.
