// Temporal integration for the TypeSafe SDK.
//
// Its own module so that importing the SDK never drags the Temporal SDK in.
//
// The Go floor here is 1.26, not the core's 1.23, because go.temporal.io/sdk v1.49
// declares it. The floor of an optional integration constrains that module,
// not anyone using the SDK without it.
module github.com/nibir1/typesafe-go/integrations/temporal

go 1.26.0

replace github.com/nibir1/typesafe-go => ../../

require github.com/nibir1/typesafe-go v0.0.0

require go.yaml.in/yaml/v3 v3.0.5 // indirect

require (
	github.com/facebookgo/clock v0.0.0-20150410010913-600d898af40a // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware/v2 v2.3.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.22.0 // indirect
	github.com/nexus-rpc/nexus-proto-annotations v0.1.0 // indirect
	github.com/nexus-rpc/sdk-go v0.7.0 // indirect
	github.com/robfig/cron v1.2.0 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/stretchr/testify v1.12.1
	go.temporal.io/api v1.63.5 // indirect
	go.temporal.io/sdk v1.49.0
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.2 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
