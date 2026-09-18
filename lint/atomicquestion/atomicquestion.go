// Package atomicquestion checks that TypeSafe questions ask one thing.
//
// Decomposition is the rule TypeSafe repeats on every pattern page: break a
// broad judgment into atomic questions, ask them together — they are evaluated
// in parallel and cost almost nothing extra — and combine the answers in code.
//
// A compound question defeats that. "Is this urgent and does it mention
// billing?" returns one probability covering two propositions, and no
// threshold can recover which half drove it. The answer is not wrong so much
// as unusable: code cannot branch on half of it.
//
// This analyzer catches the compound question at build time, which is where it
// is cheap, rather than in review or in a confusing production result.
//
// # On false positives
//
// A linter that cries wolf gets disabled, and a disabled linter catches
// nothing. Every rule here is deliberately narrow: it fires on constant
// strings only, requires whole-word matches, and skips anything it cannot
// evaluate statically. Instructions computed at runtime are not diagnosed,
// because nothing true can be said about them.
package atomicquestion

import (
	"fmt"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/nibir1/typesafe-go/lint/internal/qast"
)

// Settings configure the checks. The defaults are chosen to be quiet.
type Settings struct {
	// MaxChoiceOptions is the option count above which a Choice is flagged.
	// Zero uses 12.
	MaxChoiceOptions int

	// MaxInstructionWords is the word count above which instructions are
	// flagged as doing too much. Zero uses 60.
	MaxInstructionWords int

	// IncludeTests also checks _test.go files. Off by default: tests
	// construct deliberate degenerate cases — a one-option Choice, a
	// compound question — precisely to assert that something handles them,
	// and flagging those is noise that trains people to ignore the linter.
	IncludeTests bool
}

func (s Settings) maxOptions() int {
	if s.MaxChoiceOptions <= 0 {
		return 12
	}
	return s.MaxChoiceOptions
}

func (s Settings) maxWords() int {
	if s.MaxInstructionWords <= 0 {
		return 60
	}
	return s.MaxInstructionWords
}

// New returns the analyzer with the given settings.
func New(s Settings) *analysis.Analyzer {
	a := &analysis.Analyzer{
		Name: "atomicquestion",
		Doc: "check that TypeSafe questions ask one thing\n\n" +
			"A compound question returns one probability covering two propositions, " +
			"and no threshold can recover which half drove it. Split it and combine " +
			"the answers in code.",
		URL:      "https://docs.typesafe.ai/patterns/composite-scoring",
		Requires: qast.Requires,
		Run:      func(pass *analysis.Pass) (any, error) { return run(pass, s) },
	}
	a.Flags.IntVar(&s.MaxChoiceOptions, "max-options", s.maxOptions(),
		"flag a Choice with more options than this")
	a.Flags.IntVar(&s.MaxInstructionWords, "max-words", s.maxWords(),
		"flag instructions longer than this many words")
	a.Flags.BoolVar(&s.IncludeTests, "include-tests", false,
		"also check _test.go files, which construct deliberate edge cases")
	return a
}

// Analyzer is the analyzer with default settings.
var Analyzer = New(Settings{})

// conjunctions join two judgments. Deliberately not "and" alone: "terms and
// conditions" is one noun phrase, and flagging it would make this unusable.
var conjunctions = []string{
	"and also", "as well as", "and whether", "or whether",
	"and does", "and is", "and has", "and was", "and will",
	"or does", "or is", "or has",
	"and then", "and if",
}

func run(pass *analysis.Pass, s Settings) (any, error) {
	qast.Each(pass, func(q qast.Question) {
		if !s.IncludeTests && isTestFile(pass, q.Pos) {
			return
		}
		suppressed, justified := qast.HasSuppression(pass, q.Pos, "atomicquestion")
		if suppressed {
			if !justified {
				pass.Reportf(q.Pos,
					"//nolint:atomicquestion without a justification: say why this question "+
						"must stay compound, so the next reader does not have to guess")
			}
			return
		}

		checkInstructions(pass, q, s)
		checkChoiceOptions(pass, q, s)
	})
	return nil, nil
}

func checkInstructions(pass *analysis.Pass, q qast.Question, s Settings) {
	text := q.InstructionsText
	if text == "" {
		// Computed at runtime. Nothing true can be said about it.
		return
	}

	// Two questions in one string. The clearest signal there is, and the
	// cheapest to act on.
	if n := strings.Count(text, "?"); n > 1 {
		pass.Report(analysis.Diagnostic{
			Pos: q.Pos,
			Message: fmt.Sprintf(
				"%s instructions ask %d questions; each returns one answer, so split them "+
					"into %d questions and combine the answers in code",
				q.Kind, n, n),
		})
		return
	}

	// A conjunction joining two judgments.
	for _, c := range conjunctions {
		if qast.ContainsPhrase(text, c) {
			pass.Report(analysis.Diagnostic{
				Pos: q.Pos,
				Message: fmt.Sprintf(
					"%s instructions join two judgments with %q; one probability cannot be "+
						"split back into its halves. Ask two questions — they are evaluated "+
						"in parallel and cost only the extra tokens",
					q.Kind, c),
			})
			return
		}
	}

	// Instructions long enough to be doing several things at once. A word
	// count rather than a token estimate: the threshold is about how much a
	// question is asking, and words are what a reader counts.
	if words := qast.Words(text); len(words) > s.maxWords() {
		pass.Report(analysis.Diagnostic{
			Pos: q.Pos,
			Message: fmt.Sprintf(
				"%s instructions are %d words; instructions this long usually describe "+
					"several judgments. Consider splitting, or move the supporting detail "+
					"into the state where it belongs",
				q.Kind, len(words)),
		})
	}
}

func checkChoiceOptions(pass *analysis.Pass, q qast.Question, s Settings) {
	if q.Kind != qast.KindChoice || q.Criteria == nil {
		return
	}
	n, ok := qast.CriteriaLen(q.Criteria)
	if !ok {
		return // built at runtime
	}
	if n > s.maxOptions() {
		pass.Report(analysis.Diagnostic{
			Pos: q.Criteria.Pos(),
			Message: fmt.Sprintf(
				"Choice offers %d options; above roughly %d the distribution spreads thin "+
					"and confidence stops being a useful signal. Consider a coarse Choice "+
					"followed by a second, narrower one",
				n, s.maxOptions()),
		})
	}
	if n == 1 {
		pass.Report(analysis.Diagnostic{
			Pos:     q.Criteria.Pos(),
			Message: "Choice offers one option, so the answer is foregone; a Noul asks this better",
		})
	}
}

func isTestFile(pass *analysis.Pass, pos token.Pos) bool {
	return strings.HasSuffix(pass.Fset.Position(pos).Filename, "_test.go")
}
