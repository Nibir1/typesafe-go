// Echo middleware for the TypeSafe SDK.
//
// Its own module so that importing the SDK never drags a web framework in.
//
// The Go floor here is 1.25, not the core's 1.23, because github.com/labstack/echo/v4 v4.15
// declares it. The floor of an optional integration constrains that module,
// not anyone using the SDK without it.
module github.com/nibir1/typesafe-go/integrations/echo

go 1.25.0

replace (
	github.com/nibir1/typesafe-go => ../../
	github.com/nibir1/typesafe-go/integrations/nethttp => ../nethttp
)

require (
	github.com/labstack/echo/v4 v4.15.4
	github.com/nibir1/typesafe-go v0.0.0
	github.com/nibir1/typesafe-go/integrations/nethttp v0.0.0
)

require (
	github.com/labstack/gommon v0.5.0 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
)
