// Command typesafe-lint runs the TypeSafe analyzers over a package.
//
//	go install github.com/nibir1/typesafe-go/lint/cmd/typesafe-lint@latest
//	typesafe-lint ./...
//
// It is also a vet tool, which is how to get these running in an existing
// build with no new step:
//
//	go vet -vettool=$(which typesafe-lint) ./...
//
// And each analyzer is exported for use as a golangci-lint v2 module plugin;
// see the plugin package alongside this one.
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/nibir1/typesafe-go/lint/atomicquestion"
	"github.com/nibir1/typesafe-go/lint/confidencecheck"
	"github.com/nibir1/typesafe-go/lint/jaggededge"
)

func main() {
	multichecker.Main(
		atomicquestion.Analyzer,
		jaggededge.Analyzer,
		confidencecheck.Analyzer,
	)
}
