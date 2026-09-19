// The analyzers live in their own module because go/analysis is in
// golang.org/x/tools, and the core SDK guarantees zero third-party
// dependencies. Splitting here is what keeps that guarantee true: anyone
// importing github.com/nibir1/typesafe-go still pulls in nothing.
//
// Go floor is 1.24 rather than the core's 1.23. Nobody imports an analyzer
// into a library, so the floor costs nothing here, and 1.24 is what
// golangci-lint's module plugin system expects.
module github.com/nibir1/typesafe-go/lint

go 1.26.0

// The analyzers match on the SDK's own types, so they track the SDK in this
// repository rather than a published version.

require golang.org/x/tools v0.50.0

require (
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
)
