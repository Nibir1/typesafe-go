package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	typesafe "github.com/nibir1/typesafe-go"
)

// requestFlags collects the ways a request can be described on the command
// line. A file is the complete form; the inline flags cover the common case of
// trying one question without writing a file first.
type requestFlags struct {
	file      string
	stateFile string
	stateText string
	model     string
	nouls     multiFlag
	choices   multiFlag
	scores    multiFlag
}

// multiFlag collects a repeatable key=value flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func (r *requestFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&r.file, "f", "", "request JSON file, or - for stdin")
	fs.StringVar(&r.stateFile, "state", "", "file holding the state (JSON, or plain text)")
	fs.StringVar(&r.stateText, "state-text", "", "the state as a literal string")
	fs.StringVar(&r.model, "model", "", "model to use (default: TYPESAFE_DEFAULT_MODEL, else jev-latest)")
	fs.Var(&r.nouls, "noul", "yes/no question as id=\"instructions\" (repeatable)")
	fs.Var(&r.choices, "choice", "choice question as id=\"opt1,opt2,opt3\" (repeatable)")
	fs.Var(&r.scores, "score", "score question as id=\"low,mid,high\" (repeatable, ordered)")
}

// build assembles a request from whichever form the caller used.
func (r *requestFlags) build() (*typesafe.SystemOneRequest, error) {
	inline := len(r.nouls)+len(r.choices)+len(r.scores) > 0

	switch {
	case r.file != "" && inline:
		return nil, errors.New("use either -f or the inline question flags, not both")
	case r.file != "":
		return r.fromFile()
	case inline:
		return r.fromFlags()
	default:
		return nil, errors.New("no request given: pass -f FILE, or build one with " +
			"--state-text plus at least one of --noul/--choice/--score")
	}
}

func (r *requestFlags) fromFile() (*typesafe.SystemOneRequest, error) {
	var raw []byte
	var err error
	if r.file == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(r.file)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", displayPath(r.file), err)
	}

	// Decode questions as raw JSON first: the file may describe a shape this
	// SDK does not model, and refusing it would make the CLI less capable than
	// curl for no reason.
	var wire struct {
		State     any                        `json:"state"`
		Model     string                     `json:"model"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", displayPath(r.file), err)
	}
	if len(wire.Questions) == 0 {
		return nil, fmt.Errorf("%s has no questions", displayPath(r.file))
	}

	req := &typesafe.SystemOneRequest{
		State:     wire.State,
		Model:     firstNonEmpty(r.model, wire.Model),
		Questions: make(map[string]typesafe.Question, len(wire.Questions)),
	}
	for id, body := range wire.Questions {
		q, err := decodeQuestion(body)
		if err != nil {
			return nil, fmt.Errorf("question %q: %w", id, err)
		}
		req.Questions[id] = q
	}
	return req, nil
}

// decodeQuestion turns a JSON question into its typed form when the type is
// one this SDK models, and into a RawQuestion otherwise.
//
// The typing matters more than it looks. RawQuestion is deliberately
// unvalidated — it exists so a caller is never blocked by this SDK lagging the
// API — but that means a file-loaded request would skip every construction-time
// check. An eleven-level Score would pass `typesafe lint` and then fail at the
// server with a 400 that names neither the question nor the limit, which is
// precisely the round trip lint exists to save.
//
// An unrecognized type still passes through untouched, so a question shape
// added after this release is usable today.
func decodeQuestion(body json.RawMessage) (typesafe.Question, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("parsing: %w", err)
	}

	switch probe.Type {
	case "noul":
		var q struct {
			Instructions any `json:"instructions"`
			Criteria     *struct {
				True  any `json:"true"`
				False any `json:"false"`
			} `json:"criteria"`
		}
		if err := json.Unmarshal(body, &q); err != nil {
			return nil, fmt.Errorf("parsing noul: %w", err)
		}
		n := typesafe.Noul{Instructions: q.Instructions}
		if q.Criteria != nil {
			n.Criteria = &typesafe.NoulCriteria{True: q.Criteria.True, False: q.Criteria.False}
		}
		return n, nil

	case "choice":
		var q struct {
			Instructions any            `json:"instructions"`
			Criteria     map[string]any `json:"criteria"`
		}
		if err := json.Unmarshal(body, &q); err != nil {
			return nil, fmt.Errorf("parsing choice: %w", err)
		}
		return typesafe.Choice{Instructions: q.Instructions, Criteria: typesafe.Options(q.Criteria)}, nil

	case "score":
		var q struct {
			Instructions any   `json:"instructions"`
			Criteria     []any `json:"criteria"`
		}
		if err := json.Unmarshal(body, &q); err != nil {
			return nil, fmt.Errorf("parsing score: %w", err)
		}
		return typesafe.Score{Instructions: q.Instructions, Criteria: typesafe.Levels(q.Criteria)}, nil

	default:
		var q map[string]any
		if err := json.Unmarshal(body, &q); err != nil {
			return nil, fmt.Errorf("parsing: %w", err)
		}
		return typesafe.RawQuestion(q), nil
	}
}

func (r *requestFlags) fromFlags() (*typesafe.SystemOneRequest, error) {
	state, err := r.state()
	if err != nil {
		return nil, err
	}

	req := &typesafe.SystemOneRequest{
		State:     state,
		Model:     r.model,
		Questions: map[string]typesafe.Question{},
	}

	for _, spec := range r.nouls {
		id, val, err := splitSpec("noul", spec)
		if err != nil {
			return nil, err
		}
		req.Questions[id] = typesafe.Noul{Instructions: val}
	}
	for _, spec := range r.choices {
		id, val, err := splitSpec("choice", spec)
		if err != nil {
			return nil, err
		}
		opts := splitList(val)
		if len(opts) < 2 {
			return nil, fmt.Errorf("choice %q needs at least two comma-separated options, got %q", id, val)
		}
		criteria := make(typesafe.Options, len(opts))
		for _, o := range opts {
			criteria[o] = nil // interpreted by its name alone
		}
		req.Questions[id] = typesafe.Choice{Criteria: criteria}
	}
	for _, spec := range r.scores {
		id, val, err := splitSpec("score", spec)
		if err != nil {
			return nil, err
		}
		levels := splitList(val)
		if len(levels) == 0 {
			return nil, fmt.Errorf("score %q needs comma-separated levels, lowest first", id)
		}
		crit := make(typesafe.Levels, len(levels))
		for i, l := range levels {
			crit[i] = l
		}
		req.Questions[id] = typesafe.Score{Criteria: crit}
	}
	return req, nil
}

// state resolves the state from --state-text or --state.
func (r *requestFlags) state() (any, error) {
	switch {
	case r.stateText != "" && r.stateFile != "":
		return nil, errors.New("use either --state or --state-text, not both")
	case r.stateText != "":
		return r.stateText, nil
	case r.stateFile == "":
		return nil, errors.New("no state given: pass --state FILE or --state-text STRING")
	}

	var raw []byte
	var err error
	if r.stateFile == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(r.stateFile)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", displayPath(r.stateFile), err)
	}

	// A state file may be JSON or plain prose. Try JSON, fall back to text —
	// both are legal states, and guessing wrong in the safe direction just
	// means the model sees a string.
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%s looks like JSON but does not parse: %w",
				displayPath(r.stateFile), err)
		}
		return v, nil
	}
	return trimmed, nil
}

// splitSpec parses id=value, which is how every inline question is given.
func splitSpec(kind, spec string) (string, string, error) {
	id, val, ok := strings.Cut(spec, "=")
	if !ok || strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("--%s expects id=value, got %q", kind, spec)
	}
	if strings.TrimSpace(val) == "" {
		return "", "", fmt.Errorf("--%s %q has an empty value", kind, id)
	}
	return strings.TrimSpace(id), val, nil
}

// splitList splits a comma-separated list, trimming and dropping blanks.
func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func displayPath(p string) string {
	if p == "-" {
		return "stdin"
	}
	return p
}
