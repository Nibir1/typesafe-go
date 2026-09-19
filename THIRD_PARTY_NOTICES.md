# Third-Party Notices

`typesafe-go` is licensed under Apache-2.0. This file is the authoritative register of
third-party code incorporated into it, and of the licenses that code carries.

**Current status: no third-party code has been incorporated.** The candidate projects
in §3 are recorded so the attribution process exists before any reused code lands, not
after. No register entry exists because nothing has been taken; when something is, it
names the upstream commit it came from.

### Dependencies, by module

| Module | Direct third-party dependencies |
|---|---|
| `github.com/nibir1/typesafe-go` (core, CLI, `typesafe-gen`) | **none** |
| `.../typesafecache` | **none** |
| `.../integrations/nethttp` | **none** |
| `.../examples` | **none** |
| `.../lint` (the analyzers) | `golang.org/x/tools` |
| `.../typesafeotel` | `go.opentelemetry.io/otel`, `/sdk`, `/trace` |
| `.../typesafeprom` | `github.com/prometheus/client_golang`, `client_model` |
| `.../integrations/gin` | `github.com/gin-gonic/gin` |
| `.../integrations/echo` | `github.com/labstack/echo/v4` |
| `.../integrations/fiber` | `github.com/gofiber/fiber/v3` |
| `.../integrations/langchaingo` | `github.com/tmc/langchaingo` |
| `.../integrations/temporal` | `go.temporal.io/sdk`, `github.com/stretchr/testify` |
| `.../integrations/mcp` | `github.com/modelcontextprotocol/go-sdk` |
| `deploy/example` (not published) | otel exporters, `client_golang` |

The core module's guarantee is checked three ways, because they fail
differently: `make deps` and `make deps-graph` catch a new `require`, and a
`depguard` lint rule catches an import added before anyone runs `go mod tidy`.
That includes `cmd/typesafe` and `cmd/typesafe-gen` — the CLI uses stdlib
`flag` rather than a framework, and the generator uses `go/parser` rather than
`golang.org/x/tools`, so neither binary has a dependency either.

Every other module is separate **so that it can** depend on something. Importing
`github.com/nibir1/typesafe-go` pulls in none of them.

### Licence audit

`make licenses` reads the licence of every third-party module the build
actually compiles — from `go list -deps`, not `go list -m all`, which includes
modules nothing touches — and fails on anything that would constrain
Apache-2.0 redistribution.

As of 2026-09-19, across every optional module: **100 third-party modules, all
permissive.** Apache-2.0, MIT, BSD-2/3-Clause and ISC. No GPL, LGPL, AGPL, MPL
or EPL anywhere in the graph.

One module ships no licence file and is allowed with its evidence recorded in
`scripts/check_licenses.py`:

| Module | Why |
|---|---|
| `github.com/nexus-rpc/nexus-proto-annotations` | MIT. The Go module is rooted at the repository's `go/` subdirectory while the `LICENSE` sits at the repository root, so the module zip omits it. Verified against GitHub's licence API on 2026-09-19 (`spdx_id: MIT`). Reached only transitively, through `go.temporal.io/sdk`. |

These are **dependencies**, not incorporated code. Nothing in §3 has been
copied, adapted, or translated into this repository.

---

## 1. The rule

Three things must happen together, in the same pull request, whenever code from another
project is incorporated — copied verbatim, adapted, or translated:

1. **A file header** on every file containing derived code:

   ```go
   // Portions of this file are derived from <project>
   // (<url>), Copyright (c) <year> <holder>, licensed under MIT.
   // See THIRD_PARTY_NOTICES.md.
   ```

2. **An entry in §3 below**, moved from "Not incorporated" to "Incorporated", naming
   exactly which files and what was taken.

3. **The upstream license text preserved** in §4.

A pull request that adds derived code without all three is not mergeable. This is
enforced by review; `make deps` independently asserts that the core module has acquired
no third-party *dependencies*, which is a related but distinct guarantee.

## 2. What counts as derived

| Situation | Attribution required? |
|---|---|
| Copied a function, type, or test verbatim | **Yes** |
| Adapted a function — renamed, reformatted, lightly edited | **Yes** |
| Translated the logic of a non-obvious algorithm | **Yes** |
| Read their code, then wrote your own from the published API contract | No |
| Chose the same field names as the wire format documents | No |
| Arrived at a similar function signature that Go idiom makes obvious | No |

When uncertain, attribute. The cost is three lines in a file nobody reads. The cost of
getting it wrong is a license violation in a published Apache-2.0 release.

**Note on independent convergence.** Six Go SDKs independently model this API almost
identically, because the wire contract is published and Go idiom is narrow. Matching
`type NoulAnswer struct { Noul float64 }` is not derivation — it is the only sensible
Go rendering of a documented JSON object. Do not attribute what the contract dictates;
do attribute what someone else *decided*.

---

## 3. Register

### Incorporated

*(none yet)*

| Upstream | Files here | What was taken | License |
|---|---|---|---|
| — | — | — | — |

### Candidates — reviewed, not incorporated

These projects were read during the source audit of 2026-09-18, recorded in the
maintainers' design notes.
Nothing has been taken from any of them. They are listed so that the copyright
information is on hand if that changes.

| Upstream | Copyright | License | Notes |
|---|---|---|---|
| [Tangerg/typesafe-sdk-go](https://github.com/Tangerg/typesafe-sdk-go) | Copyright (c) 2026 Tangerg | MIT | Sealed entry types, `omitzero` tags, documented nil-semantics |
| [2389-research/typesafe-go](https://github.com/2389-research/typesafe-go) | Copyright (c) 2026 2389 Research, Inc. | MIT | Ordered score accessors, partial cassette support |
| [cole-gillespie/typesafe-go](https://github.com/cole-gillespie/typesafe-go) | Copyright (c) 2026 typesafe-go contributors | MIT | `MaxRetryAfter` cap on retry-after |
| [fgn/jevgo](https://github.com/fgn/jevgo) | Copyright (c) 2026 Fredrik Gustafsson | MIT | Sentinel error set, optional tracer interface |
| [zhirschtritt/typesafe-go](https://github.com/zhirschtritt/typesafe-go) | Copyright (c) 2026 Zach Hirschtritt | MIT | Typed criteria containers, retry policy validation |
| [Stumble/jev-go](https://github.com/Stumble/jev-go) | Copyright (c) 2026 Stumble | MIT | Per-call retry overrides, `cmd/` entry point |

---

## 4. License texts

All projects listed in §3 are MIT-licensed. The MIT terms below apply to each of them
under its own copyright line as recorded above.

```
MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

> When an entry moves to "Incorporated", replace this shared block with that project's
> verbatim `LICENSE` file contents, fetched from its repository at the commit the code
> was taken from. Record that commit SHA in the register entry.

---

## 5. Compatibility

MIT is permissive and compatible with Apache-2.0 redistribution. Incorporating MIT code
into this Apache-2.0 project is permitted provided the copyright notice and permission
notice travel with it — which is what §1 enforces. The combined work is distributed
under Apache-2.0; the incorporated portions remain under MIT.
