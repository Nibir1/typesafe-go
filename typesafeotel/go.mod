// OpenTelemetry tracing lives in its own module because go.opentelemetry.io
// is a large dependency tree and the core SDK guarantees an empty one.
// Importing github.com/nibir1/typesafe-go still pulls in nothing.
//
// The Go floor here is 1.25, not the core's 1.23, because go.opentelemetry.io
// /otel v1.46 declares it. Pinning an older otel to reach 1.23 would mean
// shipping instrumentation against a superseded API to protect a floor that
// only matters for the library itself — nobody is blocked from using the SDK
// by the version requirement of an optional tracing module.
module github.com/nibir1/typesafe-go/typesafeotel

go 1.25.0

require (
	github.com/nibir1/typesafe-go v1.0.0
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
