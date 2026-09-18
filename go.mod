// Module typesafe-go is a community-maintained Go SDK for the TypeSafe
// System One API (Jev). It is not affiliated with TypeSafe AI.
//
// The core module has no third-party dependencies and must stay that way:
// see docs/Dev_Roadmap.md principle P1. Optional features (observability,
// caching, CLI, analyzers, integrations) live in separate modules.
//
// Go floor is 1.23 — the lowest version providing log/slog, slices/maps,
// per-iteration loop variables, net/http pattern routing, and iter.Seq
// without third-party shims. See docs/Dev_Roadmap.md section 4.
module github.com/nibir1/typesafe-go

go 1.23
