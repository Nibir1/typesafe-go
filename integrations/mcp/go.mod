// MCP server exposing TypeSafe to LLM agents.
//
// Its own module so that importing the SDK never drags the MCP SDK in. Built
// on github.com/modelcontextprotocol/go-sdk, the official Go SDK, stable since
// v1.0.0 and maintained with Google.
//
// The Go floor here is 1.25, not the core's 1.23, because github.com/modelcontextprotocol/go-sdk v1.8
// declares it. The floor of an optional integration constrains that module,
// not anyone using the SDK without it.
module github.com/nibir1/typesafe-go/integrations/mcp

go 1.25.0

require github.com/nibir1/typesafe-go v1.0.0

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
