// Package statepath resolves the backticked state references that TypeSafe's
// documentation recommends writing inside question instructions.
//
// When a state is a structured object, the docs advise naming the relevant
// part by path, in backticks:
//
//	"Does `ticket.messages[0].text` request a refund?"
//
// This is a prompting convention, not a wire feature. The server performs no
// path resolution — the model is simply trained to read a backticked path as a
// pointer into the state. Which means a typo is silent: the model sees a path
// naming nothing, answers anyway, and the answer quietly degrades.
//
// That silence is what this package exists to break. Every reference is
// extracted and resolved against the actual state before the request is sent,
// so a misspelling surfaces as a warning at the call site instead of as a
// mysteriously poor answer in production.
package statepath

import (
	"fmt"
	"strconv"
	"strings"
)

// Reference is one backticked path found in a question.
type Reference struct {
	// Path is the reference with its backticks stripped.
	Path string

	// Resolved reports whether Path names something in the state.
	Resolved bool

	// Reason explains an unresolved path: which segment failed and why.
	Reason string
}

// Extract returns every backticked reference in v, in order of appearance,
// with duplicates removed.
//
// v may be a string, or any nested map or slice — instructions and criteria
// both accept structured values, and a path may be written inside either.
//
// Not every backticked span is a path. Prose such as `extracted_value` names a
// sibling key in a structured instruction, and code-ish fragments appear in
// rubric text. Extract returns all of them and leaves the judgment to Resolve,
// which reports only what genuinely fails to resolve.
func Extract(v any) []string {
	seen := make(map[string]bool)
	var out []string
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case string:
			for _, p := range backticked(t) {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		case map[string]any:
			for _, k := range sortedKeys(t) {
				walk(t[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// backticked pulls the contents of every `...` span out of s.
func backticked(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			return out
		}
		rest := s[i+1:]
		j := strings.IndexByte(rest, '`')
		if j < 0 {
			return out
		}
		if inner := strings.TrimSpace(rest[:j]); inner != "" {
			out = append(out, inner)
		}
		s = rest[j+1:]
	}
}

// Resolve walks path against state and reports whether it names anything.
//
// The grammar is the one the documentation uses: dot-separated keys with
// bracketed integer indices, e.g. ticket.messages[0].text.
//
// A path resolving to a JSON null counts as resolved: the key exists, and its
// value is null. Only a missing key or an out-of-range index is unresolved.
func Resolve(state any, path string) (any, bool, string) {
	segs, err := parse(path)
	if err != nil {
		return nil, false, err.Error()
	}
	cur := state
	for i, seg := range segs {
		switch s := seg.(type) {
		case key:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false, fmt.Sprintf("%s is not an object, so %q cannot be read from it",
					pathPrefix(segs, i), string(s))
			}
			v, ok := m[string(s)]
			if !ok {
				return nil, false, fmt.Sprintf("%s has no key %q", pathPrefix(segs, i), string(s))
			}
			cur = v
		case index:
			a, ok := cur.([]any)
			if !ok {
				return nil, false, fmt.Sprintf("%s is not an array, so [%d] cannot be read from it",
					pathPrefix(segs, i), int(s))
			}
			if int(s) < 0 || int(s) >= len(a) {
				return nil, false, fmt.Sprintf("%s has %d element(s), so [%d] is out of range",
					pathPrefix(segs, i), len(a), int(s))
			}
			cur = a[int(s)]
		}
	}
	return cur, true, ""
}

// Check resolves every path-shaped reference found in v against state, and
// returns those that name nothing.
//
// Only multi-segment references are considered — see looksLikePath for why a
// bare `identifier` is left alone.
func Check(state, v any) []Reference {
	var out []Reference
	for _, p := range Extract(v) {
		if !looksLikePath(p) {
			continue
		}
		if _, ok, reason := Resolve(state, p); !ok {
			out = append(out, Reference{Path: p, Resolved: false, Reason: reason})
		}
	}
	return out
}

// looksLikePath decides whether a backticked span was meant as a state path.
//
// It requires at least two segments, or an index: something containing "." or
// "[]". A bare identifier is deliberately ignored.
//
// That is a precision/recall trade made on the evidence. TypeSafe's own
// documentation uses backticks for more than state paths — a structured
// instruction of the form
//
//	{"field": {...}, "extracted_value": "4471",
//	 "question": "Does `extracted_value` match the `field` in `source_text`?"}
//
// backticks two keys of the *instruction object* alongside one key of the
// state. Ordinary emphasis ("is this `urgent`?") is common too. Warning on
// every bare identifier would fire constantly on correct input, and a checker
// that cries wolf is one nobody leaves switched on.
//
// The cost is a missed single-segment typo: `refund_polcy` for `refund_policy`
// passes unremarked. The realistic failure this guards against is the deep
// path — `ticket.messages[0].mesage` — where there is more to get wrong and
// the intent is unambiguous.
func looksLikePath(p string) bool {
	if p == "" || strings.ContainsAny(p, " \t\n\"'{}()") {
		return false
	}
	if !strings.ContainsAny(p, ".[") {
		return false
	}
	segs, err := parse(p)
	return err == nil && len(segs) > 1
}

type segment any
type key string
type index int

func parse(path string) ([]segment, error) {
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}
	var segs []segment
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			return nil, fmt.Errorf("empty segment in %q", path)
		}
		name := part
		if i := strings.IndexByte(part, '['); i >= 0 {
			name = part[:i]
			rest := part[i:]
			for len(rest) > 0 {
				if rest[0] != '[' {
					return nil, fmt.Errorf("unexpected %q in %q", rest, path)
				}
				end := strings.IndexByte(rest, ']')
				if end < 0 {
					return nil, fmt.Errorf("unclosed [ in %q", path)
				}
				n, err := strconv.Atoi(rest[1:end])
				if err != nil {
					return nil, fmt.Errorf("non-integer index %q in %q", rest[1:end], path)
				}
				if name != "" {
					segs = append(segs, key(name))
					name = ""
				}
				segs = append(segs, index(n))
				rest = rest[end+1:]
			}
			continue
		}
		segs = append(segs, key(name))
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("no segments in %q", path)
	}
	return segs, nil
}

// pathPrefix renders the portion of the path already traversed, for an error
// message that says where the walk stopped rather than only that it did.
func pathPrefix(segs []segment, upto int) string {
	if upto == 0 {
		return "the state"
	}
	var b strings.Builder
	for _, s := range segs[:upto] {
		switch v := s.(type) {
		case key:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(string(v))
		case index:
			fmt.Fprintf(&b, "[%d]", int(v))
		}
	}
	return "`" + b.String() + "`"
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Insertion sort: these maps are small and this avoids importing sort.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
