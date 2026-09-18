// Package lint exposes the TypeSafe analyzers for embedding.
//
// Three ways to run them:
//
//	# standalone
//	typesafe-lint ./...
//
//	# as a vet tool, with no new build step
//	go vet -vettool=$(which typesafe-lint) ./...
//
//	# as a golangci-lint v2 module plugin
//	golangci-lint custom
//
// For the golangci-lint route, add this module to .custom-gcl.yml and
// reference New from your plugin settings. The v2 module plugin system links
// analyzers into a custom binary rather than loading them at runtime, so the
// version of golangci-lint and the version of these analyzers are pinned
// together — which is the point.
package lint

import (
	"golang.org/x/tools/go/analysis"

	"github.com/nibir1/typesafe-go/lint/atomicquestion"
	"github.com/nibir1/typesafe-go/lint/confidencecheck"
	"github.com/nibir1/typesafe-go/lint/jaggededge"
)

// Settings configure the analyzers when they are embedded.
type Settings struct {
	// MaxChoiceOptions is the option count above which a Choice is flagged.
	MaxChoiceOptions int `json:"max-choice-options"`

	// MaxInstructionWords is the word count above which instructions are
	// flagged. Zero uses the default.
	MaxInstructionWords int `json:"max-instruction-words"`
}

// All returns every analyzer, configured.
//
// The signature matches what golangci-lint's module plugin system expects, so
// the same function serves an embedding host and a plain multichecker.
func All(s Settings) []*analysis.Analyzer {
	return []*analysis.Analyzer{
		atomicquestion.New(atomicquestion.Settings{
			MaxChoiceOptions:    s.MaxChoiceOptions,
			MaxInstructionWords: s.MaxInstructionWords,
		}),
		jaggededge.Analyzer,
		confidencecheck.Analyzer,
	}
}

// New returns the analyzers with default settings.
func New() []*analysis.Analyzer { return All(Settings{}) }
