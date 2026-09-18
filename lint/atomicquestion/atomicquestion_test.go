package atomicquestion_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/nibir1/typesafe-go/lint/atomicquestion"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), atomicquestion.Analyzer, "a")
}
