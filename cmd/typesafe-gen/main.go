// Command typesafe-gen generates typed TypeSafe questions from Go enums.
//
// It reads a struct whose fields are tagged `typesafe:"choice"` or
// `typesafe:"score"`, finds the enum each field's type declares, and emits the
// question constructors and typed answer accessors for them.
//
//	//go:generate typesafe-gen -type TicketQuestions
//
//	// TicketQuestions is everything asked about one support ticket.
//	type TicketQuestions struct {
//	    // Which team should handle this?
//	    Department Topic `typesafe:"choice"`
//
//	    // Rate incident severity.
//	    Severity Severity `typesafe:"score"`
//	}
//
// # Why generate this
//
// The option set exists twice in a hand-written integration: once as the Go
// enum the code switches on, and once as the criteria map the request carries.
// They drift, and nothing reports it — the model answers with an option the
// switch has no arm for, and the request goes out missing an option the code
// believes it offered. Generating the question from the enum makes the enum
// the only source, so the two cannot disagree.
//
// Descriptions come from each constant's doc comment. A constant with no doc
// comment gets a null description, which the API reads as "interpret this
// option by its name alone" — legal, and occasionally what you want.
//
// The generator is deliberately strict about enum shape. A Score level's
// position is its score, so a score enum must be declared with iota or with
// explicit integer literals, lowest first; anything else is rejected rather
// than guessed at, because a wrong guess maps every answer to the wrong label
// and nothing in the result reveals it.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Exit codes, matching the typesafe CLI's convention.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr *os.File) int {
	fs := flag.NewFlagSet("typesafe-gen", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		typeName = fs.String("type", "", "name of the tagged struct type (required)")
		dir      = fs.String("dir", ".", "directory holding the package")
		out      = fs.String("o", "", "output file (default: <type>_typesafe.go, lower-cased)")
	)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: typesafe-gen -type <StructName> [-dir .] [-o out.go]

Generates typed TypeSafe questions from a struct whose fields are tagged
`+"`typesafe:\"choice\"`"+` or `+"`typesafe:\"score\"`"+`.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *typeName == "" {
		fs.Usage()
		return exitUsage
	}

	src, err := generate(*dir, *typeName)
	if err != nil {
		fmt.Fprintf(stderr, "typesafe-gen: %v\n", err)
		return exitFailure
	}

	path := *out
	if path == "" {
		path = filepath.Join(*dir, strings.ToLower(*typeName)+"_typesafe.go")
	}
	if err := os.WriteFile(path, src, 0o644); err != nil {
		fmt.Fprintf(stderr, "typesafe-gen: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// --- model -------------------------------------------------------------------

// enumValue is one constant of an enum type.
type enumValue struct {
	Const       string // the Go constant's name
	Wire        string // the option name, for a choice
	Index       int    // the level index, for a score
	Description string // from the constant's doc comment; "" means null
}

// question is one tagged field.
type question struct {
	Field        string // the struct field's name
	ID           string // the question id sent on the wire
	Kind         string // "choice" or "score"
	EnumType     string // the field's named type
	Instructions string // from the field's doc comment
	Values       []enumValue
}

// Generate is the generator's whole job, exposed so tests can run it without a
// subprocess.
func Generate(dir, typeName string) ([]byte, error) { return generate(dir, typeName) }

func generate(dir, typeName string) ([]byte, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		// Skip this generator's own previous output, so a regeneration reads
		// the declarations rather than what it wrote last time.
		return !strings.HasSuffix(fi.Name(), "_test.go") &&
			!strings.HasSuffix(fi.Name(), "_typesafe.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", dir, err)
	}

	pkg, pkgName, err := singlePackage(pkgs)
	if err != nil {
		return nil, err
	}

	strct, structDoc, err := findStruct(pkg, typeName)
	if err != nil {
		return nil, err
	}

	questions, err := collectQuestions(pkg, typeName, strct)
	if err != nil {
		return nil, err
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("%s has no fields tagged `typesafe:\"choice\"` or `typesafe:\"score\"`", typeName)
	}

	return render(pkgName, typeName, structDoc, questions)
}

// singlePackage rejects a directory holding more than one package, rather than
// picking one and generating against declarations the caller cannot see.
func singlePackage(pkgs map[string]*ast.Package) (*ast.Package, string, error) {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Strings(names)

	switch len(names) {
	case 0:
		return nil, "", fmt.Errorf("no Go package found")
	case 1:
		return pkgs[names[0]], names[0], nil
	default:
		return nil, "", fmt.Errorf("directory holds %d packages (%s); "+
			"run the generator in a directory with one", len(names), strings.Join(names, ", "))
	}
}

func findStruct(pkg *ast.Package, name string) (*ast.StructType, string, error) {
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != name {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return nil, "", fmt.Errorf("%s is not a struct type", name)
				}
				doc := ts.Doc
				if doc == nil {
					doc = gd.Doc
				}
				return st, docText(doc), nil
			}
		}
	}
	return nil, "", fmt.Errorf("type %s not found", name)
}

func collectQuestions(pkg *ast.Package, structName string, st *ast.StructType) ([]question, error) {
	var out []question

	for _, field := range st.Fields.List {
		if field.Tag == nil {
			continue
		}
		tag, err := strconv.Unquote(field.Tag.Value)
		if err != nil {
			continue
		}
		spec, ok := reflect.StructTag(tag).Lookup("typesafe")
		if !ok {
			continue
		}

		kind, opts := splitTag(spec)
		switch kind {
		case "choice", "score":
		case "-", "":
			continue
		default:
			return nil, fmt.Errorf("unknown typesafe tag %q; want \"choice\" or \"score\"", kind)
		}

		if len(field.Names) != 1 {
			return nil, fmt.Errorf("a tagged field must declare exactly one name")
		}
		fieldName := field.Names[0].Name

		ident, ok := field.Type.(*ast.Ident)
		if !ok {
			return nil, fmt.Errorf("field %s: the type of a tagged field must be a named type "+
				"declared in this package", fieldName)
		}

		q := question{
			Field:        fieldName,
			ID:           opts["id"],
			Kind:         kind,
			EnumType:     ident.Name,
			Instructions: docText(field.Doc),
		}
		if q.ID == "" {
			q.ID = snakeCase(fieldName)
		}
		if q.Instructions == "" {
			return nil, fmt.Errorf("field %s has no doc comment; the comment is the "+
				"question's instructions, so there is nothing to ask", fieldName)
		}

		vals, err := collectEnum(pkg, ident.Name, kind)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", fieldName, err)
		}
		q.Values = vals
		out = append(out, q)
	}

	// Field order is the struct's own; ids must be unique or one question
	// silently replaces another in the request map.
	seen := map[string]string{}
	for _, q := range out {
		if prev, dup := seen[q.ID]; dup {
			return nil, fmt.Errorf("fields %s and %s both use the question id %q; "+
				"one would replace the other in the request", prev, q.Field, q.ID)
		}
		seen[q.ID] = q.Field
	}

	// Go forbids a method and a field with the same name, so the generated
	// accessors must not collide with any field of the struct — including
	// untagged ones. Catching it here beats emitting code that does not
	// compile and leaving the caller to work out which of the two names is
	// generated.
	fields := map[string]bool{}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			fields[n.Name] = true
		}
	}
	for _, q := range out {
		for _, method := range []string{q.Field + "Question", q.Field + "Answer"} {
			if fields[method] {
				return nil, fmt.Errorf("field %s would generate a method named %s, "+
					"but %s is already a field; Go forbids a method and a field with "+
					"the same name, so rename one of them", q.Field, method, method)
			}
		}
	}
	if fields["Questions"] {
		return nil, fmt.Errorf("a field named Questions collides with the generated " +
			"Questions method; rename it")
	}
	return out, nil
}

// splitTag parses `choice,id=department` into its kind and options.
func splitTag(spec string) (kind string, opts map[string]string) {
	opts = map[string]string{}
	parts := strings.Split(spec, ",")
	kind = strings.TrimSpace(parts[0])
	for _, p := range parts[1:] {
		k, v, found := strings.Cut(strings.TrimSpace(p), "=")
		if found {
			opts[k] = v
		}
	}
	return kind, opts
}

// collectEnum finds every constant of the named type, in source order.
func collectEnum(pkg *ast.Package, typeName, kind string) ([]enumValue, error) {
	// Files come out of a map, so visit them in a fixed order or the enum's
	// order — which for a Score *is* its meaning — depends on map iteration.
	names := make([]string, 0, len(pkg.Files))
	for name := range pkg.Files {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []enumValue
	for _, name := range names {
		for _, decl := range pkg.Files[name].Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			vals, err := constBlock(gd, typeName, kind, len(out))
			if err != nil {
				return nil, err
			}
			out = append(out, vals...)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no constants of type %s found", typeName)
	}
	return out, nil
}

// constBlock reads one `const (...)` group.
//
// iotaIndex is the position within the block, which is what an omitted value
// repeats. base is how many values of this type were already found, so a score
// enum split across two blocks still numbers continuously.
func constBlock(gd *ast.GenDecl, typeName, kind string, base int) ([]enumValue, error) {
	var (
		out         []enumValue
		currentType string
		iotaForm    bool
	)

	for iotaIndex, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}

		// A spec with no type inherits the previous one's, which is how an
		// iota block declares its type exactly once.
		if vs.Type != nil {
			if id, ok := vs.Type.(*ast.Ident); ok {
				currentType = id.Name
			} else {
				currentType = ""
			}
		}
		if currentType != typeName {
			continue
		}
		if len(vs.Names) != 1 {
			return nil, fmt.Errorf("constant declarations of %s must name one constant each", typeName)
		}
		name := vs.Names[0].Name
		if name == "_" {
			continue
		}

		v := enumValue{
			Const:       name,
			Description: describe(name, docText(vs.Doc), docText(vs.Comment)),
		}

		switch kind {
		case "choice":
			lit, err := stringValue(vs)
			if err != nil {
				return nil, fmt.Errorf("constant %s: %w", name, err)
			}
			v.Wire = lit

		case "score":
			// An omitted value repeats the previous expression, which for an
			// iota block means the position.
			if len(vs.Values) == 0 {
				if !iotaForm {
					return nil, fmt.Errorf("constant %s has no value and the block does not use iota", name)
				}
				v.Index = iotaIndex
			} else if isIota(vs.Values[0]) {
				iotaForm = true
				v.Index = iotaIndex
			} else if n, err := intValue(vs); err == nil {
				v.Index = n
			} else {
				return nil, fmt.Errorf("constant %s: %w; a Score level's position is its "+
					"score, so the enum must be declared with iota or explicit integer "+
					"literals, lowest first", name, err)
			}
			v.Index += base
		}

		out = append(out, v)
	}

	if kind == "score" {
		for i, v := range out {
			if want := base + i; v.Index != want {
				return nil, fmt.Errorf("constant %s has value %d but sits at position %d; "+
					"a Score level's position is its score, so the enum must be declared "+
					"lowest first with values matching their positions", v.Const, v.Index, want)
			}
		}
	}
	return out, nil
}

func isIota(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "iota"
}

func stringValue(vs *ast.ValueSpec) (string, error) {
	if len(vs.Values) != 1 {
		return "", fmt.Errorf("a choice constant needs exactly one string value")
	}
	lit, ok := vs.Values[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", fmt.Errorf("a choice constant's value must be a string literal")
	}
	return strconv.Unquote(lit.Value)
}

func intValue(vs *ast.ValueSpec) (int, error) {
	if len(vs.Values) != 1 {
		return 0, fmt.Errorf("a score constant needs exactly one integer value")
	}
	lit, ok := vs.Values[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, fmt.Errorf("a score constant's value must be an integer literal")
	}
	return strconv.Atoi(lit.Value)
}

// --- text --------------------------------------------------------------------

func docText(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	return strings.TrimSpace(g.Text())
}

// describe turns a constant's doc comment into an option description.
//
// Go doc comments start with the identifier ("TopicBilling covers ..."), which
// reads wrong as a description sent to a model. The identifier is stripped and
// the remainder capitalized. A constant with no comment gets "", which is
// emitted as a null description.
func describe(name, doc, lineComment string) string {
	if doc == "" {
		doc = lineComment
	}
	if doc == "" {
		return ""
	}
	// Collapse a multi-line comment onto one line.
	doc = strings.Join(strings.Fields(doc), " ")

	if rest, found := strings.CutPrefix(doc, name+" "); found {
		for _, copula := range []string{"is ", "are ", "means "} {
			if trimmed, ok := strings.CutPrefix(rest, copula); ok {
				rest = trimmed
				break
			}
		}
		doc = rest
	}
	if doc == "" {
		return ""
	}
	r := []rune(doc)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// snakeCase converts a Go field name to a wire-friendly id.
func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			// Do not split an acronym: "APIKey" becomes "api_key", not
			// "a_p_i_key".
			prevLower := i > 0 && unicode.IsLower(rune(s[i-1]))
			nextLower := i+1 < len(s) && unicode.IsLower(rune(s[i+1]))
			if i > 0 && (prevLower || nextLower) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// --- rendering ---------------------------------------------------------------

func render(pkgName, typeName, structDoc string, questions []question) ([]byte, error) {
	var b bytes.Buffer

	fmt.Fprintf(&b, "// Code generated by typesafe-gen. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	fmt.Fprintf(&b, "import typesafe %q\n\n", "github.com/nibir1/typesafe-go")

	// One AllX slice per enum, emitted once even if two fields share a type.
	emitted := map[string]bool{}
	for _, q := range questions {
		if emitted[q.EnumType] {
			continue
		}
		emitted[q.EnumType] = true

		fmt.Fprintf(&b, "// All%s lists every declared %s, in declaration order.\n",
			q.EnumType, q.EnumType)
		fmt.Fprintf(&b, "var All%s = []%s{\n", q.EnumType, q.EnumType)
		for _, v := range q.Values {
			fmt.Fprintf(&b, "\t%s,\n", v.Const)
		}
		fmt.Fprintf(&b, "}\n\n")
	}

	for _, q := range questions {
		switch q.Kind {
		case "choice":
			fmt.Fprintf(&b, "// %sQuestion is the %q question: %s\n",
				q.Field, q.ID, q.Instructions)
			fmt.Fprintf(&b, "func (%s) %sQuestion() typesafe.TypedChoiceQuestion[%s] {\n",
				typeName, q.Field, q.EnumType)
			fmt.Fprintf(&b, "\treturn typesafe.TypedChoice[%s](%q,\n", q.EnumType, q.Instructions)
			for _, v := range q.Values {
				if v.Description == "" {
					fmt.Fprintf(&b, "\t\ttypesafe.OptionOf(%s, nil),\n", v.Const)
					continue
				}
				fmt.Fprintf(&b, "\t\ttypesafe.OptionOf(%s, %q),\n", v.Const, v.Description)
			}
			fmt.Fprintf(&b, "\t)\n}\n\n")

			fmt.Fprintf(&b, "// %sAnswer decodes the %q answer, checked against the declared option set.\n",
				q.Field, q.ID)
			fmt.Fprintf(&b, "func (q %s) %sAnswer(r *typesafe.SystemOneResponse) (typesafe.ChoiceAnswerOf[%s], error) {\n",
				typeName, q.Field, q.EnumType)
			fmt.Fprintf(&b, "\treturn q.%sQuestion().Answer(r, %q)\n}\n\n", q.Field, q.ID)

		case "score":
			fmt.Fprintf(&b, "// %sQuestion is the %q question: %s\n",
				q.Field, q.ID, q.Instructions)
			fmt.Fprintf(&b, "func (%s) %sQuestion() typesafe.TypedScoreQuestion[%s] {\n",
				typeName, q.Field, q.EnumType)
			fmt.Fprintf(&b, "\treturn typesafe.TypedScore[%s](%q,\n", q.EnumType, q.Instructions)
			for _, v := range q.Values {
				if v.Description == "" {
					fmt.Fprintf(&b, "\t\ttypesafe.LevelOf(%s, nil),\n", v.Const)
					continue
				}
				fmt.Fprintf(&b, "\t\ttypesafe.LevelOf(%s, %q),\n", v.Const, v.Description)
			}
			fmt.Fprintf(&b, "\t)\n}\n\n")

			fmt.Fprintf(&b, "// %sAnswer decodes the %q answer.\n", q.Field, q.ID)
			fmt.Fprintf(&b, "func (q %s) %sAnswer(r *typesafe.SystemOneResponse) (typesafe.ScoreAnswerOf[%s], error) {\n",
				typeName, q.Field, q.EnumType)
			fmt.Fprintf(&b, "\treturn q.%sQuestion().Answer(r, %q)\n}\n\n", q.Field, q.ID)
		}
	}

	// Questions returns the whole set, which is what goes into a request.
	if structDoc != "" {
		fmt.Fprintf(&b, "// Questions returns every question declared by %s.\n//\n// %s\n",
			typeName, structDoc)
	} else {
		fmt.Fprintf(&b, "// Questions returns every question declared by %s.\n", typeName)
	}
	fmt.Fprintf(&b, "func (q %s) Questions() typesafe.Questions {\n", typeName)
	fmt.Fprintf(&b, "\treturn typesafe.Questions{\n")
	for _, qq := range questions {
		fmt.Fprintf(&b, "\t\t%q: q.%sQuestion(),\n", qq.ID, qq.Field)
	}
	fmt.Fprintf(&b, "\t}\n}\n")

	src, err := format.Source(b.Bytes())
	if err != nil {
		// Emitting unformattable code is a generator bug; show what it wrote
		// rather than only the parse error, or the bug is undebuggable.
		return nil, fmt.Errorf("generated code does not parse: %w\n\n%s", err, b.String())
	}
	return src, nil
}
