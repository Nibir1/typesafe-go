// Package fixtures locates and loads the golden contract fixtures.
//
// Several suites need them — the offline contract checks under tests/contract,
// the live drift replay under tests/integration — and they sit at different
// depths below the module root. Resolving the path with a relative string in
// each package means every new suite gets to rediscover how many "../" it
// needs, and a wrong count fails as "no fixtures found" rather than as
// something legible.
//
// This resolves the directory from this file's own compiled-in path instead,
// so it is correct from any package at any depth.
package fixtures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Root returns the module root directory.
func Root() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	// This file lives at <root>/internal/fixtures/fixtures.go.
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// ContractDir returns the golden contract fixture directory.
func ContractDir() string { return filepath.Join(Root(), "testdata", "contract") }

// SpecPath returns the vendored OpenAPI document.
func SpecPath() string { return filepath.Join(Root(), "testdata", "spec", "openapi.json") }

// TB is the subset of *testing.T this package needs, declared structurally so
// that importing it never pulls the testing package into a binary.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Fixture is one golden request/response pair, decoded into the generic JSON
// shape with numbers preserved as json.Number.
type Fixture struct {
	// Name is the fixture's file prefix, e.g. "04_mixed_three".
	Name string

	// Request and Response are the decoded golden files.
	Request  map[string]any
	Response map[string]any

	// RequestPath and ResponsePath are the files they came from.
	RequestPath  string
	ResponsePath string
}

// All loads every golden fixture, sorted by name.
func All(tb TB) []Fixture {
	tb.Helper()
	dir := ContractDir()
	paths, err := filepath.Glob(filepath.Join(dir, "*.request.json"))
	if err != nil {
		tb.Fatalf("fixtures: glob %s: %v", dir, err)
	}
	if len(paths) == 0 {
		tb.Fatalf("fixtures: none found in %s", dir)
	}
	sort.Strings(paths)

	out := make([]Fixture, 0, len(paths))
	for _, reqPath := range paths {
		respPath := strings.TrimSuffix(reqPath, ".request.json") + ".response.json"
		out = append(out, Fixture{
			Name:         strings.TrimSuffix(filepath.Base(reqPath), ".request.json"),
			Request:      ReadJSON(tb, reqPath),
			Response:     ReadJSON(tb, respPath),
			RequestPath:  reqPath,
			ResponsePath: respPath,
		})
	}
	return out
}

// ReadJSON decodes a JSON file, keeping numbers as json.Number so that
// comparisons see the literal the file holds rather than a float64 round trip.
func ReadJSON(tb TB, path string) map[string]any {
	tb.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("fixtures: read %s: %v", path, err)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		tb.Fatalf("fixtures: parse %s: %v", path, err)
	}
	return m
}
