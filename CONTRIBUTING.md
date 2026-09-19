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

Maintainers only. **The order matters and is not negotiable**, for a reason
worth understanding before you start.

Every submodule carries, for local development:

```
replace github.com/nibir1/typesafe-go => ../
require github.com/nibir1/typesafe-go v0.0.0
```

**Go ignores a `replace` in a dependency's go.mod.** Publish that file as-is and
a consumer running `go get github.com/nibir1/typesafe-go/typesafecache@v1.0.0`
reads it, ignores the replace, looks for `typesafe-go v0.0.0` on the proxy, does
not find it, and cannot build. The module is tagged, published and unusable.

So the root has to be published *first*, and the submodules re-pointed at it
before they are tagged.

```bash
# 1. Changelog, by hand, describing what changed for a user.
$EDITOR CHANGELOG.md

# 2. Everything passes.
make verify

# 3. Tag and push the root.
git tag v1.2.3 && git push origin v1.2.3

# 4. Wait for the module proxy to see it — a minute or two.
GOPROXY=https://proxy.golang.org go list -m github.com/nibir1/typesafe-go@v1.2.3

# 5. Re-point every submodule at the published version.
make release-prep VERSION=v1.2.3
for m in lint typesafecache typesafeotel typesafeprom integrations/*; do
  (cd "$m" && go mod tidy && go test ./...)
done

# 6. Confirm nothing unpublishable is left.
make release-check VERSION=v1.2.3

# 7. Commit, then tag each submodule.
git commit -am "Pin submodules to v1.2.3"
git tag typesafecache/v1.2.3 && git push origin typesafecache/v1.2.3
# ... and so on. nethttp before gin, echo and fiber, which depend on it.

# 8. Back to development.
make release-revert VERSION=v1.2.3
git commit -am "Restore development replaces"
```

The Release workflow handles the rest: it verifies the tagged commit, runs
`govulncheck` over every module, builds, signs with keyless Sigstore, attaches
an SBOM and attests build provenance.

A tag is not the place to discover a failing test. The full gate runs before
anything is built, and the release will not start without it.

### Why there is no `go.work`

A workspace would let local builds resolve the submodules without any
`replace`, which would make all of the above unnecessary. It is not committed
because a workspace takes the **highest** `go` directive of any module in it —
1.26 here, from `lint` and `integrations/temporal`. Anyone on Go 1.23 could then
not build the repository at all, which breaks both the declared floor and
`make verify` for exactly the contributors the floor exists to serve.

`go.work` is gitignored. Create one locally if you want it.
