// Package jaggededge flags questions that ask Jev to do something TypeSafe
// documents it as bad at.
//
// This is the difference between a linter and an opinion. TypeSafe publishes a
// "Jev 1.13 jaggedness" page listing nine named failure modes with recommended
// fixes — counting, date arithmetic, numeric encodings, indirection,
// contradictory criteria, generation, and so on. Every rule here cites one, and
// every diagnostic repeats the documented remedy.
//
// The failures this catches are quiet ones. Asking the model to count items in
// a list returns a plausible number that is wrong more often as the list grows;
// asking it which of two dates comes first returns a confident answer with no
// signal that the question was unsuitable. Nothing in the response says so. The
// only place to catch these is before they are sent.
//
//	https://docs.typesafe.ai/model-jaggedness/jev-1.13
//
// # Scope
//
// Rules fire on constant strings only, and match whole words. Instructions
// computed at runtime are skipped: no static check can say anything true about
// them, and guessing would produce exactly the false positives that get a
// linter switched off.
package jaggededge

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/nibir1/typesafe-go/lint/internal/qast"
)

// Analyzer flags questions that hit a documented failure mode.
var Analyzer = &analysis.Analyzer{
	Name: "jaggededge",
	Doc: "flag questions that hit a documented Jev failure mode\n\n" +
		"Each rule cites a section of TypeSafe's published model-jaggedness notes " +
		"and repeats its recommended fix.",
	URL:      "https://docs.typesafe.ai/model-jaggedness/jev-1.13",
	Requires: qast.Requires,
	Run:      run,
}

// includeTests also checks _test.go files. Off by default, for the same reason
// as the other analyzers: test fixtures are deliberate edge cases.
var includeTests bool

func init() {
	Analyzer.Flags.BoolVar(&includeTests, "include-tests", false,
		"also check _test.go files, which construct deliberate edge cases")
}

// rule is one documented failure mode.
type rule struct {
	// section is the jaggedness heading this comes from. Named in every
	// diagnostic, so a reader can go and read the source.
	section string

	// phrases trigger the rule as whole-word matches.
	phrases []string

	// message explains the failure and the documented remedy.
	message string
}

var rules = []rule{
	{
		section: "Math and Numbers: Counting",
		phrases: []string{
			"how many", "number of", "count the", "count how",
			"total number", "how often", "how many times",
		},
		message: "Jev does not count reliably — it recognizes the shape of an answer " +
			"rather than tallying, and the error grows with the size of the thing " +
			"being counted. Ask one Noul per item and sum the answers in code",
	},
	{
		section: "Date and time comparison",
		phrases: []string{
			"which date", "earlier than", "later than", "before or after",
			"how many days", "how long between", "days between", "which came first",
			"is after", "is before", "date range", "within the last",
		},
		message: "Jev reads dates as text, not as ordered quantities, so comparing or " +
			"ordering them is unreliable. Extract the parts as a Choice over enumerated " +
			"options, then compare in code",
	},
	{
		section: "Math and Numbers: Numeric representations",
		phrases: []string{
			"hex", "hexadecimal", "rgb", "rgba", "hex code", "color code",
			"binary", "assembly", "bytecode", "base64",
		},
		message: "Jev performs better on semantic representations than numeric ones — " +
			"it cannot reliably judge whether two hex values are near each other. " +
			"Convert in code and pass a named bucket instead",
	},
	{
		section: "Indirection",
		phrases: []string{
			"not un", "never not", "is not false", "is not untrue",
			"doesn't not", "does not not", "isn't not",
		},
		message: "Double negatives cost accuracy: instructions carrying them are answered " +
			"less reliably. State the condition positively",
	},
	{
		section: "Generation",
		phrases: []string{
			"write a", "generate a", "summarize", "summarize", "rewrite",
			"paraphrase", "translate", "compose a", "draft a", "explain why",
			"describe the", "list the",
		},
		message: "Jev returns typed decisions, not text. Use a generative model for this " +
			"step and give Jev the narrow judgment about its output",
	},
	{
		section: "Math and Numbers",
		phrases: []string{
			"calculate", "compute the", "add up", "subtract", "multiply",
			"divide", "what percentage", "what is the sum", "average of",
		},
		message: "Jev is not a calculator. Keep arithmetic in code and give the model the " +
			"judgment that surrounds it",
	},
}

func run(pass *analysis.Pass) (any, error) {
	qast.Each(pass, func(q qast.Question) {
		if !includeTests && isTestFile(pass, q.Pos) {
			return
		}
		suppressed, justified := qast.HasSuppression(pass, q.Pos, "jaggededge")
		if suppressed {
			if !justified {
				pass.Reportf(q.Pos,
					"//nolint:jaggededge without a justification: say why this question is "+
						"worth the documented risk")
			}
			return
		}

		checkPhrases(pass, q)
		checkInvertedNoulCriteria(pass, q)
		checkLargeLiteralState(pass, q)
	})
	return nil, nil
}

func checkPhrases(pass *analysis.Pass, q qast.Question) {
	text := q.InstructionsText
	if text == "" {
		return
	}
	for _, r := range rules {
		for _, p := range r.phrases {
			if !qast.ContainsPhrase(text, p) {
				continue
			}
			pass.Report(analysis.Diagnostic{
				Pos: q.Pos,
				Message: fmt.Sprintf("%s instructions contain %q. %s (jaggedness: %s)",
					q.Kind, p, r.message, r.section),
			})
			return // one diagnostic per question; the rest is noise
		}
	}
}

// checkInvertedNoulCriteria catches a Noul whose true and false descriptions
// argue the opposite way round.
//
// From the jaggedness page, section "Contradictory instructions and criteria":
// a Noul where true maps to no and false maps to yes performs measurably
// worse. It is an easy mistake to make and an impossible one to see in a
// probability — the answer just comes back subtly wrong.
func checkInvertedNoulCriteria(pass *analysis.Pass, q qast.Question) {
	if q.Kind != qast.KindNoul || q.Criteria == nil {
		return
	}
	lit, ok := unwrapUnary(q.Criteria).(*ast.CompositeLit)
	if !ok {
		return
	}

	var trueText, falseText string
	var truePos = q.Pos
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "True":
			trueText = qast.StringValue(pass, kv.Value)
			truePos = kv.Value.Pos()
		case "False":
			falseText = qast.StringValue(pass, kv.Value)
		}
	}
	if trueText == "" || falseText == "" {
		return
	}

	// The signal: the "true" description reads as a negation while the
	// "false" one does not. Narrow on purpose — this must not fire on a
	// legitimately negative-sounding condition where both sides agree.
	if negated(trueText) && !negated(falseText) {
		pass.Report(analysis.Diagnostic{
			Pos: truePos,
			Message: fmt.Sprintf(
				"Noul criteria look inverted: true describes the absence of something "+
					"(%q) while false describes its presence. A Noul whose true maps to "+
					"\"no\" performs measurably worse. Swap them, or restate the "+
					"instructions so the polarity matches (jaggedness: Contradictory "+
					"instructions and criteria)",
				truncate(trueText, 48)),
		})
	}
}

var negationWords = []string{"no", "not", "never", "without", "absent", "lacks", "none"}

func negated(text string) bool {
	for _, w := range qast.Words(text) {
		for _, n := range negationWords {
			if w == n {
				return true
			}
		}
	}
	return false
}

// checkLargeLiteralState flags a very large string literal used as a question's
// instructions, which usually means state has been pasted into the question.
//
// From "Large state full of irrelevant detail": accuracy falls as unrelated
// content grows, and a large blob makes it harder to tell which part produced
// a wrong answer. Content belongs in the state; the question should name the
// part it cares about.
func checkLargeLiteralState(pass *analysis.Pass, q qast.Question) {
	const threshold = 2000 // characters
	if len(q.InstructionsText) > threshold {
		pass.Report(analysis.Diagnostic{
			Pos: q.Pos,
			Message: fmt.Sprintf(
				"%s instructions are %d characters — this looks like content that belongs "+
					"in the state. Accuracy falls as a question carries unrelated detail; "+
					"put the material in the state and name the relevant part from the "+
					"instructions (jaggedness: Large state full of irrelevant detail)",
				q.Kind, len(q.InstructionsText)),
		})
	}
}

func unwrapUnary(e ast.Expr) ast.Expr {
	if u, ok := e.(*ast.UnaryExpr); ok {
		return u.X
	}
	return e
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func isTestFile(pass *analysis.Pass, pos token.Pos) bool {
	return strings.HasSuffix(pass.Fset.Position(pos).Filename, "_test.go")
}
