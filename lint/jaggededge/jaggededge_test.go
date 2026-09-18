package jaggededge_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/nibir1/typesafe-go/lint/jaggededge"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), jaggededge.Analyzer, "a")
}
