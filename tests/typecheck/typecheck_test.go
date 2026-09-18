// Package typecheck asserts that the typed question API rejects at compile
// time what the untyped one can only discover at runtime.
//
// These are negative-compilation tests: each case is a program that must NOT
// build. A normal test cannot express that — the file would have to compile to
// be part of the test binary — so each program is written to a temporary
// module and handed to the toolchain.
//
// The check runs the real compiler rather than go/types directly. A go/types
// run would need to resolve this module's import path from source, which is
// exactly the part most likely to go wrong in a way that makes the test pass
// for the wrong reason: a package that fails to load produces errors too, and
// a negative test that cannot tell "wrong type" from "could not find package"
// asserts nothing.
package typecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot is this file's ../.. — the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this source file")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s: %v", root, err)
	}
	return root
}

// build writes src into a throwaway module and compiles it, returning the
// combined output and whether it succeeded.
func build(t *testing.T, src string) (string, bool) {
	t.Helper()

	dir := t.TempDir()
	root := repoRoot(t)

	// Forward slashes: a replace directive is a module path expression, and
	// the Windows leg of the CI matrix would otherwise write backslashes.
	gomod := "module typecheck\n\ngo 1.23\n\n" +
		"require github.com/nibir1/typesafe-go v0.0.0\n\n" +
		"replace github.com/nibir1/typesafe-go => " + filepath.ToSlash(root) + "\n"

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	// The SDK has no third-party dependencies, so this never needs the
	// network. Turning the proxy off makes that a checked property rather
	// than an assumption, and keeps the test runnable on an air-gapped box.
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=-mod=mod")

	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

const header = `package main

import typesafe "github.com/nibir1/typesafe-go"

type Topic string

const (
	TopicBilling   Topic = "billing"
	TopicTechnical Topic = "technical"
)

type Severity int

const (
	SevLow Severity = iota
	SevHigh
)

func main() {
`

const footer = "\n}\n"

func program(body string) string { return header + body + footer }

// The control: the same shapes as the negative cases, but correct. If this
// fails to build, every negative case below is passing for the wrong reason.
func TestValidProgramCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes the compiler")
	}
	out, ok := build(t, program(`
	q := typesafe.TypedChoice[Topic]("Which team?",
		typesafe.OptionOf(TopicBilling, "Payments"),
		typesafe.OptionOf(TopicTechnical, "Bugs"),
	)
	_ = q

	s := typesafe.TypedScore[Severity]("How bad?",
		typesafe.LevelOf(SevLow, "low"),
		typesafe.LevelOf(SevHigh, "high"),
	)
	_ = s

	var ans typesafe.ChoiceAnswerOf[Topic]
	switch ans.Choice {
	case TopicBilling:
	case TopicTechnical:
	}
	_ = ans.Probabilities[TopicBilling]

	var sa typesafe.ScoreAnswerOf[Severity]
	_ = sa.AtOrAbove(SevHigh)
`))
	if !ok {
		t.Fatalf("the valid program must compile; the negative cases below mean\n"+
			"nothing if it does not:\n%s", out)
	}
}

func TestRejectedAtCompileTime(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes the compiler")
	}

	cases := []struct {
		name string
		body string
		// want is a fragment of the compiler's complaint. Asserting on it is
		// what separates "rejected for the reason we claim" from "rejected
		// because the program was malformed some other way".
		want string
	}{
		{
			name: "a bare string is not an option key",
			body: `
	_ = typesafe.TypedChoice[Topic]("Which team?",
		typesafe.OptionOf("biling", "typo"),
	)`,
			want: "TypedOption[string]",
		},
		{
			name: "comparing an answer against an undeclared string",
			body: `
	var ans typesafe.ChoiceAnswerOf[Topic]
	if ans.Choice == "biling" {
	}`,
			// An untyped string constant converts to Topic, so this one does
			// compile in Go's type system. It is listed here to record that
			// the guarantee is about *keys and switch arms built from the
			// enum*, not about every string literal.
			want: "",
		},
		{
			name: "indexing a typed distribution with the wrong enum",
			body: `
	var ans typesafe.ChoiceAnswerOf[Topic]
	_ = ans.Probabilities[SevLow]`,
			want: "cannot use SevLow",
		},
		{
			name: "mixing option types in one question",
			body: `
	_ = typesafe.TypedChoice[Topic]("Which team?",
		typesafe.OptionOf(TopicBilling, "Payments"),
		typesafe.OptionOf(SevLow, "wrong type entirely"),
	)`,
			want: "",
		},
		{
			name: "a score level is not an option key",
			body: `
	var sa typesafe.ScoreAnswerOf[Severity]
	_ = sa.AtOrAbove(TopicBilling)`,
			want: "cannot use TopicBilling",
		},
		{
			name: "an int is not a valid option key type",
			body: `
	_ = typesafe.TypedChoice[int]("Which team?")`,
			want: "does not satisfy",
		},
		{
			name: "a string is not a valid level key type",
			body: `
	_ = typesafe.TypedScore[string]("How bad?")`,
			want: "does not satisfy",
		},
		{
			name: "Exhaustive against the wrong enum",
			body: `
	var ans typesafe.ChoiceAnswerOf[Topic]
	_ = typesafe.Exhaustive(ans, SevLow)`,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := build(t, program(tc.body))

			// One case is documented as legal; assert that too, so the list
			// records what the type system does and does not buy.
			if tc.name == "comparing an answer against an undeclared string" {
				if !ok {
					t.Fatalf("an untyped string constant converts to Topic, so this "+
						"should compile:\n%s", out)
				}
				return
			}

			if ok {
				t.Fatalf("this compiled, but should not have:\n%s", tc.body)
			}
			if tc.want != "" && !strings.Contains(out, tc.want) {
				t.Errorf("rejected, but not for the expected reason.\nwant a mention of %q, got:\n%s",
					tc.want, out)
			}
			// However it failed, it must not be because the package could
			// not be found — that would make every case pass vacuously.
			for _, bogus := range []string{"no required module", "cannot find module", "unknown import path"} {
				if strings.Contains(out, bogus) {
					t.Fatalf("the module did not resolve, so this test asserts nothing:\n%s", out)
				}
			}
		})
	}
}
