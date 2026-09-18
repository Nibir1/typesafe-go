// Fiber v3 middleware for the TypeSafe SDK.
//
// Its own module so that importing the SDK never drags a web framework in.
//
// The Go floor here is 1.25, not the core's 1.23, because github.com/gofiber/fiber/v3 v3.5
// declares it. The floor of an optional integration constrains that module,
// not anyone using the SDK without it.
module github.com/nibir1/typesafe-go/integrations/fiber

go 1.25.0

replace (
	github.com/nibir1/typesafe-go => ../../
	github.com/nibir1/typesafe-go/integrations/nethttp => ../nethttp
)

require (
	github.com/gofiber/fiber/v3 v3.5.0
	github.com/nibir1/typesafe-go v0.0.0
	github.com/nibir1/typesafe-go/integrations/nethttp v0.0.0
)

require (
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/gofiber/schema v1.8.3 // indirect
	github.com/gofiber/utils/v2 v2.4.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.73.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)
