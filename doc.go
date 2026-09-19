// Package typesafe is a community-maintained Go SDK for the TypeSafe
// System One API and its model, Jev.
//
// It is not affiliated with, endorsed by, or sponsored by TypeSafe AI.
//
// # Status
//
// Phase 0 (contract lock). The wire contract is pinned in docs/WIRE_CONTRACT.md
// and testdata/spec/openapi.json; golden request/response pairs live in
// testdata/contract. The client, question primitives, and typed answers arrive
// in Phases 1 and 2. See docs/Dev_Roadmap.md.
//
// # The API in one paragraph
//
// You send one state — a string, object, or array — together with a map of
// named questions, and receive one typed answer per question in a single call.
// Three question types cover the decision space: Noul (yes/no, answered with
// the probability of yes), Choice (select one option from a set you define),
// and Score (rate against ordered levels you define). Answers are constrained
// to the options you supplied, so a successful response cannot contain a value
// your code did not ask for.
//
// # Design
//
// The core module has no third-party dependencies and never will. Observability,
// caching, the CLI, the static analyzers, and framework integrations live in
// separate modules so that importing this one costs nothing.
package typesafe

import "runtime/debug"

// Version is the SDK version, reported in the User-Agent header.
//
// A var rather than a const so a release build can stamp it with
// -ldflags "-X github.com/nibir1/typesafe-go.Version=v1.0.0". The linker's -X
// flag only writes to variables; as a const this was unsettable, and a
// released binary would have reported 0.0.0-dev forever.
//
// A module installed with `go install ...@v1.0.0` gets its real version from
// the build info instead, which is why this is a fallback rather than the
// source of truth. See VersionString.
var Version = "0.0.0-dev"

// VersionString returns the version this binary or module was built as.
//
// Prefers the version the Go toolchain recorded — which `go install
// module@version` sets automatically and correctly — and falls back to
// Version, which a release build stamps with -ldflags. Preferring build info
// means a user who installed with `go install` sees the version they asked
// for, not whatever the last person to edit this file typed.
func VersionString() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return Version
}

// Wire constants, fixed by the published contract. See docs/WIRE_CONTRACT.md.
const (
	// DefaultBaseURL is the API root. Overridable per client, and by the
	// TYPESAFE_BASE_URL environment variable.
	DefaultBaseURL = "https://api.typesafe.ai"

	// DefaultModel matches the official Python and JavaScript SDKs.
	DefaultModel = "jev-latest"

	// SystemOnePath is the evaluation endpoint.
	SystemOnePath = "/v1/systemone"

	// ModelsPath lists the model names this account may send.
	ModelsPath = "/v1/models"
)

// Environment variables, named to match the official SDKs so that a process
// configured for Python or JavaScript works unchanged here.
const (
	EnvAPIKey       = "TYPESAFE_API_KEY"
	EnvBaseURL      = "TYPESAFE_BASE_URL"
	EnvDefaultModel = "TYPESAFE_DEFAULT_MODEL"
	EnvLogLevel     = "TYPESAFE_LOG_LEVEL"
)

// RequestIDHeader carries a per-call identifier worth quoting in a support
// ticket. Present on both successful and failed responses.
const RequestIDHeader = "x-typesafe-request-id"

// Question type discriminators, used as the "type" field on both questions
// and their corresponding answers.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
	TypeScore  = "score"
)
