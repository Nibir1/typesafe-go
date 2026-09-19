// Prometheus metrics live in their own module, so the core SDK's empty
// dependency graph stays empty. Importing github.com/nibir1/typesafe-go
// still pulls in nothing.
module github.com/nibir1/typesafe-go/typesafeprom

go 1.25.0

replace github.com/nibir1/typesafe-go => ../

require (
	github.com/nibir1/typesafe-go v0.0.0
	github.com/prometheus/client_golang v1.24.1
	github.com/prometheus/client_model v0.6.3
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
