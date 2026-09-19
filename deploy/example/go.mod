// The demo service for deploy/docker-compose.yml. It is its own module so the
// SDK's module graph is not affected by an example's dependencies.
module github.com/nibir1/typesafe-go/deploy/example

go 1.25.0

replace (
	github.com/nibir1/typesafe-go => ../../
	github.com/nibir1/typesafe-go/typesafecache => ../../typesafecache
	github.com/nibir1/typesafe-go/typesafeotel => ../../typesafeotel
	github.com/nibir1/typesafe-go/typesafeprom => ../../typesafeprom
)

require (
	github.com/nibir1/typesafe-go v1.0.0
	github.com/nibir1/typesafe-go/typesafecache v0.0.0-00010101000000-000000000000
	github.com/nibir1/typesafe-go/typesafeotel v0.0.0-00010101000000-000000000000
	github.com/nibir1/typesafe-go/typesafeprom v0.0.0-00010101000000-000000000000
	github.com/prometheus/client_golang v1.24.1
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.46.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/grpc v1.83.1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
