// Package qast finds TypeSafe question literals in a Go syntax tree.
//
// Every analyzer in this module needs the same thing: given a file, locate the
// places where somebody writes a question, and hand back the instructions and
// criteria in a form that can be inspected. Doing that once, correctly, beats
// three analyzers each pattern-matching on identifier names.
//
// # Why types and not names
//
// A question is recognized by its *type*, resolved through the type checker,
// not by a composite literal that happens to be called Noul. A local struct
// named Noul in unrelated code is not a TypeSafe question and must not be
// diagnosed; a question written as `ts.Noul{...}` under an import alias is one
// and must be. Only the type checker knows the difference.
package qast

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// SDKPath is the import path whose types identify a TypeSafe question.
const SDKPath = "github.com/nibir1/typesafe-go"

// Kind is which primitive a question is.
type Kind string

// The three primitives.
const (
	KindNoul   Kind = "Noul"
	KindChoice Kind = "Choice"
	KindScore  Kind = "Score"
)

// Question is one question literal found in the source.
type Question struct {
	// Kind is which primitive it is.
	Kind Kind

	// Lit is the composite literal itself, for reporting position.
	Lit *ast.CompositeLit

	// Instructions is the expression assigned to the Instructions field, or
	// nil when the field was omitted.
	Instructions ast.Expr

	// InstructionsText is the instructions when they are a string constant
	// the compiler can evaluate, including concatenations of literals. Empty
	// when the value is computed at runtime, which is the honest signal that
	// no static check can say anything about it.
	InstructionsText string

	// Criteria is the expression assigned to the Criteria field, or nil.
	Criteria ast.Expr

	// Pos is where to anchor a diagnostic: the instructions if present, the
	// literal otherwise.
	Pos token.Pos
}

// Requires is the analyzer dependency every analyzer in this module needs.
var Requires = []*analysis.Analyzer{inspect.Analyzer}

// Each calls fn for every TypeSafe question literal in the pass.
func Each(pass *analysis.Pass, fn func(Question)) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return
	}

	insp.Preorder([]ast.Node{(*ast.CompositeLit)(nil)}, func(n ast.Node) {
		lit := n.(*ast.CompositeLit)
		kind, ok := kindOf(pass, lit)
		if !ok {
			return
		}

		q := Question{Kind: kind, Lit: lit, Pos: lit.Pos()}
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
			case "Instructions":
				q.Instructions = kv.Value
				q.InstructionsText = StringValue(pass, kv.Value)
				q.Pos = kv.Value.Pos()
			case "Criteria":
				q.Criteria = kv.Value
			}
		}
		fn(q)
	})
}

// kindOf reports which primitive a composite literal constructs, if any.
func kindOf(pass *analysis.Pass, lit *ast.CompositeLit) (Kind, bool) {
	tv, ok := pass.TypesInfo.Types[lit]
	if !ok {
		return "", false
	}
	named, ok := deref(tv.Type).(*types.Named)
	if !ok {
		return "", false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != SDKPath {
		return "", false
	}
	switch Kind(obj.Name()) {
	case KindNoul:
		return KindNoul, true
	case KindChoice:
		return KindChoice, true
	case KindScore:
		return KindScore, true
	default:
		return "", false
	}
}

func deref(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

// StringValue returns the constant string an expression evaluates to, or "".
//
// Constant folding matters here: instructions are frequently written across
// several lines joined with +, and a checker that only understood a single
// BasicLit would silently skip exactly the long instructions most worth
// checking.
func StringValue(pass *analysis.Pass, e ast.Expr) string {
	if e == nil {
		return ""
	}
	tv, ok := pass.TypesInfo.Types[e]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return ""
	}
	return constant.StringVal(tv.Value)
}

// IsTypeSafeType reports whether a type is the named type from the SDK.
func IsTypeSafeType(t types.Type, name string) bool {
	named, ok := deref(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == SDKPath && obj.Name() == name
}

// CriteriaLen returns the number of entries in a criteria literal, and whether
// it could be counted. A criteria built at runtime cannot be, and a checker
// that guessed would be wrong in the direction of false positives.
func CriteriaLen(e ast.Expr) (int, bool) {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return 0, false
	}
	return len(lit.Elts), true
}

// CriteriaEntries returns the key/value pairs of a Choice criteria literal, or
// the elements of a Score criteria literal, as constant strings where they are
// constant.
func CriteriaEntries(pass *analysis.Pass, e ast.Expr) []Entry {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	out := make([]Entry, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			out = append(out, Entry{
				Key:   StringValue(pass, kv.Key),
				Value: StringValue(pass, kv.Value),
				Expr:  kv.Value,
				Pos:   kv.Pos(),
			})
			continue
		}
		out = append(out, Entry{
			Value: StringValue(pass, elt),
			Expr:  elt,
			Pos:   elt.Pos(),
		})
	}
	return out
}

// Entry is one criteria element.
type Entry struct {
	// Key is the option name for a Choice, empty for a Score level.
	Key string

	// Value is the description, when it is a constant string.
	Value string

	// Expr is the value expression.
	Expr ast.Expr

	// Pos is where to anchor a diagnostic.
	Pos token.Pos
}

// Words splits text into lowercase words, dropping punctuation.
//
// Shared so that every rule tokenizes identically: a rule matching "and" and a
// rule matching "and," disagreeing about the same sentence is the sort of
// inconsistency that erodes trust in a linter.
func Words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '\'')
	})
}

// ContainsPhrase reports whether text contains phrase as whole words.
func ContainsPhrase(text, phrase string) bool {
	words := Words(text)
	target := Words(phrase)
	if len(target) == 0 || len(words) < len(target) {
		return false
	}
	for i := 0; i+len(target) <= len(words); i++ {
		match := true
		for j := range target {
			if words[i+j] != target[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// HasSuppression reports whether a //nolint comment names this analyzer for
// the node at pos, and whether that suppression carries a justification.
//
// The escape hatch requires a reason: a bare suppression is an unanswered
// question in the code, and why it was added is exactly what the next reader
// needs. An unjustified one is itself diagnosed.
//
// Comments are matched on the node's own line or on any of the few lines above
// it. A question literal spans several lines, and the //nolint is written above
// the declaration rather than above the field that happens to be diagnosed —
// an earlier version checked only line-1 and silently ignored every
// suppression on a multi-line literal.
func HasSuppression(pass *analysis.Pass, pos token.Pos, analyzer string) (bool, bool) {
	file := fileFor(pass, pos)
	if file == nil {
		return false, false
	}
	line := pass.Fset.Position(pos).Line

	// A literal's fields sit a few lines below the declaration it is attached
	// to; four covers the realistic shapes without reaching into unrelated code.
	const lookback = 4

	for _, group := range file.Comments {
		for _, c := range group.List {
			cl := pass.Fset.Position(c.Pos()).Line
			if cl > line || cl < line-lookback {
				continue
			}
			text := c.Text
			marker := "//nolint:" + analyzer
			idx := strings.Index(text, marker)
			if idx < 0 {
				continue
			}
			rest := strings.TrimSpace(text[idx+len(marker):])
			rest = strings.TrimPrefix(rest, "//")
			return true, strings.TrimSpace(rest) != ""
		}
	}
	return false, false
}

func fileFor(pass *analysis.Pass, pos token.Pos) *ast.File {
	for _, f := range pass.Files {
		if f.Pos() <= pos && pos <= f.End() {
			return f
		}
	}
	return nil
}
