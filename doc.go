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

// Version is the SDK version, reported in the User-Agent header.
const Version = "0.0.0-dev"

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
