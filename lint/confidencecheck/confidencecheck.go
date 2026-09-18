// Package confidencecheck flags code that acts on an answer without consulting
// its confidence, and code that reaches for a confidence that does not exist.
//
// TypeSafe's confidence guidance is a three-path pattern: act automatically
// when the model is confident, proceed with a check when it is moderately
// confident, and refuse to act when it is telling you it does not know. Code
// that branches on `answer.Choice` alone collapses all three into one, which
// is fine until the day the model is unsure and the system acts anyway.
//
// The second rule is the sharper one. A Noul carries no confidence field — the
// probability is the uncertainty — so `resp.Confidence(id)` on a Noul reports
// absence, and anything that treats a missing confidence as zero reads every
// Noul as maximally uncertain. The SDK returns (float64, bool) precisely so
// this is visible; this analyzer catches the code that ignores the bool.
//
//	https://docs.typesafe.ai/confidence
package confidencecheck

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"

	"github.com/nibir1/typesafe-go/lint/internal/qast"
)

// Analyzer flags unchecked confidence and impossible confidence reads.
// Analyzer is the analyzer with default settings.
var Analyzer = New()

// New returns a fresh analyzer, so that two hosts can configure it
// independently rather than sharing one package-level flag set.
func New() *analysis.Analyzer {
	var includeTests bool
	a := &analysis.Analyzer{
		Name: "confidencecheck",
		Doc: "flag decisions made without consulting confidence\n\n" +
			"Also flags reading a confidence off a Noul answer, which has none: the " +
			"probability is the uncertainty.",
		URL:      "https://docs.typesafe.ai/confidence",
		Requires: qast.Requires,
		Run: func(pass *analysis.Pass) (any, error) {
			return run(pass, includeTests)
		},
	}
	a.Flags.BoolVar(&includeTests, "include-tests", false,
		"also check _test.go files, which construct deliberate edge cases")
	return a
}

func run(pass *analysis.Pass, includeTests bool) (any, error) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, nil
	}

	checkDiscardedConfidenceOK(pass, insp, includeTests)
	checkBareThreshold(pass, insp, includeTests)
	checkBranchWithoutConfidence(pass, insp, includeTests)
	return nil, nil
}

// checkDiscardedConfidenceOK catches `conf, _ := resp.Confidence(id)`.
//
// The second result is the only thing distinguishing "the model is completely
// unsure" from "this primitive has no confidence at all". Discarding it makes
// a Noul look like a zero-confidence Choice, and the code then escalates every
// Noul or, worse, acts on one it thinks is uncertain.
func checkDiscardedConfidenceOK(pass *analysis.Pass, insp *inspector.Inspector, includeTests bool) {
	insp.Preorder([]ast.Node{(*ast.AssignStmt)(nil)}, func(n ast.Node) {
		if !includeTests && isTestFile(pass, n.Pos()) {
			return
		}
		as := n.(*ast.AssignStmt)
		if len(as.Rhs) != 1 || len(as.Lhs) != 2 {
			return
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || !isConfidenceCall(pass, call) {
			return
		}
		blank, ok := as.Lhs[1].(*ast.Ident)
		if !ok || blank.Name != "_" {
			return
		}
		if suppressed, _ := qast.HasSuppression(pass, as.Pos(), "confidencecheck"); suppressed {
			return
		}
		pass.Report(analysis.Diagnostic{
			Pos: as.Lhs[1].Pos(),
			Message: "discarding the second result of Confidence hides the difference between " +
				"a confidence of zero and a Noul, which carries no confidence at all. " +
				"Check it: a discarded false reads every Noul as maximally uncertain",
		})
	})
}

func isConfidenceCall(pass *analysis.Pass, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Confidence" {
		return false
	}
	// Only the SDK's method, not any method named Confidence.
	if sig, ok := pass.TypesInfo.Types[call.Fun].Type.(*types.Signature); ok {
		if recv := sig.Recv(); recv != nil {
			return qast.IsTypeSafeType(recv.Type(), "SystemOneResponse")
		}
	}
	tv, ok := pass.TypesInfo.Types[sel.X]
	return ok && qast.IsTypeSafeType(tv.Type, "SystemOneResponse")
}

// checkBareThreshold catches `ans.Bool(0.8)` with an unnamed literal.
//
// A threshold is a policy decision about what a wrong answer costs. Written as
// a bare number at a call site it becomes 0.8 here, 0.75 there, and 0.9 in the
// retry path within a year, with nothing to tie them together or explain any of
// them. Naming it makes it reviewable.
func checkBareThreshold(pass *analysis.Pass, insp *inspector.Inspector, includeTests bool) {
	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		if !includeTests && isTestFile(pass, n.Pos()) {
			return
		}
		call := n.(*ast.CallExpr)
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || len(call.Args) != 1 {
			return
		}
		if sel.Sel.Name != "Bool" && sel.Sel.Name != "Uncertain" {
			return
		}
		tv, ok := pass.TypesInfo.Types[sel.X]
		if !ok || !qast.IsTypeSafeType(tv.Type, "NoulAnswer") {
			return
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return // already a named constant or an expression
		}
		if suppressed, _ := qast.HasSuppression(pass, call.Pos(), "confidencecheck"); suppressed {
			return
		}
		pass.Report(analysis.Diagnostic{
			Pos: call.Args[0].Pos(),
			Message: fmt.Sprintf(
				"threshold %s is a bare literal. A threshold encodes what a wrong answer "+
					"costs, and an unnamed one drifts apart from its siblings. Give it a "+
					"named constant",
				lit.Value),
		})
	})
}

// checkBranchWithoutConfidence catches code that *branches* on a Choice or
// Score answer in a function that never consults its confidence.
//
// # Precision
//
// An earlier version fired on any read of .Choice or .Score, and lit up the
// SDK's own source: accessors returning a.Score, a computation dividing it,
// the implementation of Nearest itself. None of those is a decision, and a
// linter that flags its own library is one nobody enables.
//
// Three narrowings, each removing a whole class of false positive:
//
//   - The read must drive control flow — an if condition, a switch tag, or a
//     case expression. Returning or computing a value is not acting on it.
//   - Methods on the answer types are skipped: a method on ScoreAnswer reading
//     a.Score is the implementation, not a caller.
//   - Test files are skipped by default, since tests construct deliberate edge
//     cases. Pass -include-tests to check them.
func checkBranchWithoutConfidence(pass *analysis.Pass, insp *inspector.Inspector, includeTests bool) {
	insp.Preorder([]ast.Node{(*ast.FuncDecl)(nil), (*ast.FuncLit)(nil)}, func(n ast.Node) {
		if !includeTests && isTestFile(pass, n.Pos()) {
			return
		}
		if fd, ok := n.(*ast.FuncDecl); ok && isAnswerMethod(pass, fd) {
			return
		}
		body := funcBody(n)
		if body == nil {
			return
		}

		// Confidence consulted anywhere in the function counts: reading it a
		// few lines before branching is the normal shape of correct code.
		var readsConfidence bool
		ast.Inspect(body, func(x ast.Node) bool {
			if sel, ok := x.(*ast.SelectorExpr); ok && sel.Sel.Name == "Confidence" {
				if tv, ok := pass.TypesInfo.Types[sel.X]; ok {
					if qast.IsTypeSafeType(tv.Type, "ChoiceAnswer") ||
						qast.IsTypeSafeType(tv.Type, "ScoreAnswer") {
						readsConfidence = true
					}
				}
			}
			return true
		})
		if readsConfidence {
			return
		}

		// Only reads inside a control-flow condition count as acting on the
		// answer.
		var first *ast.SelectorExpr
		ast.Inspect(body, func(x ast.Node) bool {
			if first != nil {
				return false
			}
			for _, cond := range conditions(x) {
				if sel := findAnswerRead(pass, cond); sel != nil {
					first = sel
					return false
				}
			}
			return true
		})
		if first == nil {
			return
		}
		if suppressed, _ := qast.HasSuppression(pass, first.Pos(), "confidencecheck"); suppressed {
			return
		}
		pass.Report(analysis.Diagnostic{
			Pos: first.Pos(),
			Message: fmt.Sprintf(
				"this branches on .%s but the function never reads its Confidence. "+
					"TypeSafe's guidance is three paths — act, confirm, escalate — and code "+
					"that ignores confidence collapses them into one, acting even when the "+
					"model reports it does not know. See decision.Bands",
				first.Sel.Name),
		})
	})
}

// conditions returns the nodes a statement uses to steer control flow.
//
// An if's init statement counts along with its condition: `if i, _ :=
// a.Nearest(); i > 1` reads the answer to decide the branch just as much as
// `if a.Score > 1` does, and only the syntax differs.
func conditions(n ast.Node) []ast.Node {
	switch s := n.(type) {
	case *ast.IfStmt:
		out := []ast.Node{s.Cond}
		if s.Init != nil {
			out = append(out, s.Init)
		}
		return out
	case *ast.SwitchStmt:
		var out []ast.Node
		if s.Tag != nil {
			out = append(out, s.Tag)
		}
		if s.Init != nil {
			out = append(out, s.Init)
		}
		return out
	case *ast.CaseClause:
		out := make([]ast.Node, 0, len(s.List))
		for _, e := range s.List {
			out = append(out, e)
		}
		return out
	case *ast.ForStmt:
		var out []ast.Node
		if s.Cond != nil {
			out = append(out, s.Cond)
		}
		if s.Init != nil {
			out = append(out, s.Init)
		}
		return out
	}
	return nil
}

// findAnswerRead returns the first read of a decision-bearing field within n.
func findAnswerRead(pass *analysis.Pass, n ast.Node) *ast.SelectorExpr {
	var found *ast.SelectorExpr
	ast.Inspect(n, func(x ast.Node) bool {
		if found != nil {
			return false
		}
		sel, ok := x.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Choice", "Score", "Nearest", "MostLikely":
		default:
			return true
		}
		tv, ok := pass.TypesInfo.Types[sel.X]
		if !ok {
			return true
		}
		if qast.IsTypeSafeType(tv.Type, "ChoiceAnswer") || qast.IsTypeSafeType(tv.Type, "ScoreAnswer") {
			found = sel
			return false
		}
		return true
	})
	return found
}

// isAnswerMethod reports whether a declaration is a method on an answer type,
// which makes it the implementation rather than a caller.
func isAnswerMethod(pass *analysis.Pass, fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return false
	}
	tv, ok := pass.TypesInfo.Types[fd.Recv.List[0].Type]
	if !ok {
		return false
	}
	for _, name := range []string{"ChoiceAnswer", "ScoreAnswer", "NoulAnswer", "SystemOneResponse"} {
		if qast.IsTypeSafeType(tv.Type, name) {
			return true
		}
	}
	return false
}

func isTestFile(pass *analysis.Pass, pos token.Pos) bool {
	return strings.HasSuffix(pass.Fset.Position(pos).Filename, "_test.go")
}

func funcBody(n ast.Node) *ast.BlockStmt {
	switch f := n.(type) {
	case *ast.FuncDecl:
		return f.Body
	case *ast.FuncLit:
		return f.Body
	default:
		return nil
	}
}
