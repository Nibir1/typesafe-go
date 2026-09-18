package decision_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDecisionMakesNoNetworkCalls enforces the property this package is
// designed around.
//
// The roadmap's original wording was "zero imports from the client package",
// which is not achievable and was the wrong target anyway: the package needs
// typesafe.NoulAnswer and friends, and duplicating those types to satisfy an
// import rule would make the API worse for no gain.
//
// What actually matters is that composing a decision cannot perform I/O. A
// policy must be replayable from recorded answers, months later, offline, with
// the same result. That is what makes a decision auditable, and it is what
// this test checks: no net, no http, no os, and no reference to the client
// type that could make a request.
func TestDecisionMakesNoNetworkCalls(t *testing.T) {
	forbiddenImports := map[string]string{
		"net":          "composing a decision must not touch the network",
		"net/http":     "composing a decision must not touch the network",
		"os":           "composing a decision must not touch the filesystem or environment",
		"os/exec":      "composing a decision must not run anything",
		"context":      "a pure computation needs no cancellation; if it does, it is doing I/O",
		"time":         "a decision that depends on the clock is not replayable",
		"math/rand":    "a decision must be deterministic",
		"math/rand/v2": "a decision must be deterministic",
	}
	// Identifiers from the parent package that imply a request.
	forbiddenIdents := []string{"NewClient", "SystemOne(", "*typesafe.Client"}

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var checked int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++

		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		file, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if why, bad := forbiddenImports[path]; bad {
				t.Errorf("%s imports %q: %s", name, path, why)
			}
		}
		for _, ident := range forbiddenIdents {
			if strings.Contains(string(src), ident) {
				// Doc comments may legitimately mention them.
				if mentionedOnlyInComments(t, fset, name, src, ident) {
					continue
				}
				t.Errorf("%s references %q; composing a decision must not make a request", name, ident)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no source files found; the check is not running")
	}
	t.Logf("checked %d source files", checked)
}

// mentionedOnlyInComments reports whether every occurrence of ident falls
// inside a comment, so documentation can discuss the client freely.
func mentionedOnlyInComments(t *testing.T, fset *token.FileSet, name string, src []byte, ident string) bool {
	t.Helper()
	file, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		return false
	}

	inComment := 0
	for _, group := range file.Comments {
		inComment += strings.Count(group.Text(), ident)
	}
	total := strings.Count(string(src), ident)
	// Comment text strips the leading "// ", so counts can differ slightly;
	// require that at least as many occurrences are accounted for.
	return inComment >= total
}

// TestDecisionHasNoGlobalState: a package whose results depend on mutable
// package-level state is not replayable, whatever its imports say.
func TestDecisionHasNoGlobalState(t *testing.T) {
	fset := token.NewFileSet()
	entries, _ := os.ReadDir(".")
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Base(name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, ident := range vs.Names {
					// Sentinel errors are immutable in practice and are the
					// idiomatic way to expose comparable failures.
					if strings.HasPrefix(ident.Name, "Err") || strings.HasPrefix(ident.Name, "_") {
						continue
					}
					t.Errorf("%s declares package-level var %q; mutable global state makes "+
						"decisions unreplayable", name, ident.Name)
				}
			}
		}
	}
}
