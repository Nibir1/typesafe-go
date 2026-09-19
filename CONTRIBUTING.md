# Contributing

## Clone to passing tests

```bash
git clone https://github.com/nibir1/typesafe-go
cd typesafe-go
make verify
```

That is the whole setup. No API key, no network beyond the module proxy, no
services. `make verify` is exactly what CI runs, so a green run locally means a
green run there.

It needs Go 1.23+ and Python 3.11+ (for the fixture, doc, link and dashboard
checks). `make` on its own lists every target.

---

## One workflow, not five

Everything — tests, linting, docs, drift detection, the live API and releases —
is `.github/workflows/ci.yml`. A `meta` job classifies the run once and every
other job states its condition in one line.

| Trigger | What runs |
|---|---|
| push to `main`, pull request | the full code gate |
| pull request | the above, plus the benchmark regression check |
| daily schedule | the live API suite |
| Monday schedule | served OpenAPI vs the vendored copy, and fixture drift |
| manual dispatch | either of the above, plus optional cassette refresh |
| a tag | the gate, `govulncheck`, then build, sign, attest, publish |

It was five files. They shared most of their setup, drifted apart in the parts
they did not share — the secret scan in the drift workflow searched two
hard-coded directories long after `make secrets` had outgrown that — and nobody
reading one could tell what the others did.

## What `make verify` checks

| | |
|---|---|
| `tidy-check` | `go.mod` is tidy |
| `fmt-check`, `integrations-fmt` | every module is formatted |
| `vet`, `vet-integration` | `go vet` under both build tags |
| `deps`, `deps-graph` | **the core still has zero dependencies** |
| `test-race` | the whole suite under `-race` |
| `contract` | fixtures match the locked wire contract |
| `fixtures` | fixtures validate against the OpenAPI schema |
| `secrets` | no credential in any committed fixture or cassette |
| `docs-check` | 100% doc coverage on exported symbols |
| `links` | every relative link in the docs resolves |
| `bench-check` | every benchmark still builds and runs |
| `dashboards` | the Grafana dashboard references metrics that exist |
| `lint-module`, `submodules`, `integrations`, `examples` | every other module |
| `analyzers` | the analyzers pass over this repository's own source |

`make live` adds the real API and needs `TYPESAFE_API_KEY`. You do not need it
to contribute.

---

## The rules that are not negotiable

**The core module has zero third-party dependencies.** Not "few". If a change
needs one, it belongs in a submodule. Three separate checks enforce this, and
they exist because this is the property the whole layout is built around.

**Every exported symbol has a doc comment.** `make docs-check` fails otherwise.
Say *why*, not *what* — the signature already says what.

**Line endings are LF.** `.gitattributes` enforces it. Go source, shell scripts and
anything compared byte for byte all need it, and a CRLF checkout on Windows is how a
generated-file drift test fails on one platform out of seven.

**No credential reaches a committed file.** `make secrets` scans every
`testdata` tree. If you record a cassette, run it before you commit.

**Wire behaviour is verified, not assumed.** If you are changing what goes on the
wire, the change needs a fixture or a live test. Several things in
[docs/WIRE_CONTRACT.md](docs/WIRE_CONTRACT.md) contradict the published docs,
and they were all found by testing rather than reading.

---

## House style

**Comments explain why.** The reader can see what the code does. What they
cannot see is the alternative you rejected and the reason.

```go
// Store a copy, so a caller mutating the response it was handed cannot
// change what the next lookup returns.
```

not

```go
// Copy the response.
```

**Errors are sentences that help.** Name what went wrong, what was expected, and
where to look. `"a Score accepts at most 10 levels, got 12 (the server rejects
more with 400; this is documented nowhere but the wire)"` beats `"invalid
score"`.

**Tests assert behaviour, not implementation.** A test that breaks when you
rename a private function is a test that costs more than it pays.

**Do not assert exact probabilities.** Jev is documented as highly consistent
but is not contractually deterministic — one example here scored 0.75 on one
run and 0.38 on another with the same input. Assert the shape, the band, or the
decision.

---

## Adding a question-design rule

The analyzers enforce TypeSafe's published jaggedness notes. A new
`jaggededge` rule must **cite the section it comes from** and repeat its
recommended fix. That is what makes it a conformance checker rather than an
opinion, and an opinion in a linter is how the linter gets switched off.

A rule that fires on this repository's own source is wrong until proven
otherwise. An earlier version produced 21 findings against correct code; three
narrowings fixed it, and one of the 21 turned out to be a real bug.

---

## Adding an example

Each lives in `examples/<name>/` with `main.go`, `main_test.go` and a
`README.md`.

`main.go` must be copy-pasteable: it calls `typesafe.NewClient()` exactly as
yours would, and passes the client to a `run` function so the test can inject a
replaying one. Test scaffolding in an example teaches the scaffolding.

**Put the real output in the README.** Run it, copy what it printed. Writing
plausible output by hand caught three wrong claims when the real runs replaced
them — including that vagueness does not lower confidence, which is the
opposite of what the example originally said.

---

## Changing a code snippet in the docs

`docs_example_test.go` holds the code from [docs/MANUAL.md](docs/MANUAL.md) and
the README **verbatim**, as an `Example` with no `// Output:` comment. The
toolchain compiles and type-checks it and never runs it.

Change a snippet in the manual, change it there too. When that file stops
compiling, the documentation is already wrong — which is the failure mode prose
has and code does not.

```bash
cd examples
go test ./... -update    # record, needs a key
go test ./...            # replay, offline
```

---

## Pull requests

- One change per pull request.
- `make verify` passes.
- A behaviour change updates the docs that describe it — `make links` will tell
  you if you broke a cross-reference.
- The commit message says *why*. If you corrected a mistake, say what it was;
  the roadmap records those and they are the most useful part of it.

Benchmarks run on every pull request and a regression over 10% fails the build.
If a regression is intended, say so in the commit message.

---

## Releasing

Maintainers only.

```bash
make release                      # dry run: shows exactly what would happen
make release VERSION=v1.0.0       # dry run for a specific version
make release VERSION=v1.0.0 CONFIRM=yes
```

**Dry run is the default**, and the script asks you to type the version before
it pushes anything. Pushing a tag is not undoable: the Go module proxy fetches
and caches it within minutes, and a version that has been published stays
published even if you delete the tag. `git tag -d` unpublishes nothing.

With no `VERSION`, the script takes the newest `## vX.Y.Z` heading from
[Release_Notes.md](Release_Notes.md). Writing the notes first and deriving the
version from them means the two cannot disagree.

### What the script does

1. Extracts the matching section of `Release_Notes.md` — this becomes the
   GitHub release body, so **what you write there is what people see**.
2. Refuses a dirty tree, a non-`main` branch, an existing tag, or a missing
   changelog entry.
3. Runs `make verify` in full.
4. Prints the plan, and stops unless `CONFIRM=yes`.
5. Tags and pushes the root, then **waits for proxy.golang.org to serve it**.
6. Runs `make release-prep`, tidies and tests all eleven submodules, and tags
   each one.
7. Restores the development replaces.

### Why the ordering is not negotiable

Every submodule carries, for local development:

```
replace github.com/nibir1/typesafe-go => ../
require github.com/nibir1/typesafe-go v0.0.0
```

**Go ignores a `replace` in a dependency's go.mod.** Publish that file as-is and
a consumer running `go get github.com/nibir1/typesafe-go/typesafecache@v1.0.0`
reads it, ignores the replace, looks for `typesafe-go v0.0.0` on the proxy, does
not find it, and cannot build. The module is tagged, published and unusable.

So the root must publish *first*, and the submodules are re-pointed at it before
they are tagged. `scripts/check_release_ready.py` fails a release that would
ship one, and so does CI on a submodule tag.

### What CI does with the tag

The tag push runs the same single workflow as everything else. It re-runs the
full gate on that exact commit, checks the tag against the module path and the
release notes, runs `govulncheck` over every module, then builds the CLI
binaries, signs them with keyless Sigstore, attaches an SBOM and attests build
provenance.

A tag is not the place to discover a failing test, so nothing is built until
the gate passes.

### When something fails after the root tag is pushed

The root tag is the one irreversible step. Everything after it is retryable,
and each failure has its own way back.

**A submodule failed to tidy, build or test.** Nothing was tagged: the script
tags tier 1 only after every module in it passes. Fix the module, commit, and
pick up where it stopped:

```bash
make release VERSION=v1.0.0 CONFIRM=yes RESUME=submodules
```

`RESUME=submodules` skips the root — it checks the tag is already on the remote
and refuses if it is not — and tolerates the dirty tree the re-pointing leaves
behind.

**The framework integrations could not resolve `integrations/nethttp`.** They
depend on it, and a `go.mod` re-pointed at `integrations/nethttp v1.0.0` cannot
resolve until the proxy is serving that tag. This is why the submodules are
tagged in two tiers with a wait between them. If the wait times out, resume
once `go list -m github.com/nibir1/typesafe-go/integrations/nethttp@v1.0.0`
answers.

**The workflow itself was the bug.** A tag run uses the workflow file from
*that tag's commit*, so re-running it re-runs the bug, and moving the tag is
not an option — see below. Fix the workflow on `main`, then run the workflow
manually with `publish_tag` set to the tag. That path runs from `main`, so it
uses the fixed file, checks out the tag's tree to build from, and adds only
what was never published: the release page, the archives, the signatures and
the SBOM. It refuses a submodule tag, and it refuses a tag that already has a
release.

### A published version cannot be taken back

Deleting a tag removes it from GitHub and from nothing else.
`proxy.golang.org` records which commit a version is at the moment it first
serves it, and `sum.golang.org` is an append-only transparency log of that
tree's hash. Both are public and permanent:

```bash
curl https://proxy.golang.org/github.com/nibir1/typesafe-go/@v/v1.0.0.info
curl https://sum.golang.org/lookup/github.com/nibir1/typesafe-go@v1.0.0
```

So re-tagging a version at a different commit does not give you a second
attempt at it. It gives you a repository that disagrees with the proxy forever,
and a `SECURITY ERROR: checksum mismatch` for everyone who already fetched the
version — which reads as a supply-chain attack, not as a tidy-up.

If a release went out incomplete, finish it where you can and release the fix
as the next patch version. Versions are cheap; trust in the checksum database
is not.

### Why there is no `go.work`

A workspace would let local builds resolve the submodules without any
`replace`, which would make all of the above unnecessary. It is not committed
because a workspace takes the **highest** `go` directive of any module in it —
1.26 here, from `lint` and `integrations/temporal`. Anyone on Go 1.23 could then
not build the repository at all, which breaks both the declared floor and
`make verify` for exactly the contributors the floor exists to serve.

`go.work` is gitignored. Create one locally if you want it.
