package typesafe

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/nibir1/typesafe-go/internal/statepath"
)

// RefWarning is a backticked state reference in a question that does not
// resolve against the state being sent.
type RefWarning struct {
	// QuestionID is the question containing the reference.
	QuestionID string

	// Path is the reference, without its backticks.
	Path string

	// Reason says which segment failed and why.
	Reason string
}

func (w RefWarning) String() string {
	return fmt.Sprintf("question %q references `%s`, which does not resolve: %s",
		w.QuestionID, w.Path, w.Reason)
}

// CheckReferences resolves every backticked state reference in every question
// against the request's state, and reports those that name nothing.
//
// TypeSafe's documentation recommends pointing a question at the relevant part
// of a structured state by path:
//
//	"Does `ticket.messages[0].text` request a refund?"
//
// The server does not resolve these. The model is trained to read them, which
// means a typo is silent: `ticket.mesage` names nothing, the model answers
// anyway, and the answer is quietly worse. Nothing in the response indicates
// that it happened.
//
// This catches it before the request is sent. It is advisory — a reference may
// legitimately point outside the state, and backticks are also used for
// ordinary emphasis — so the result is warnings, never an error.
//
//	for _, w := range req.CheckReferences() {
//	    log.Warn(w.String())
//	}
func (r *SystemOneRequest) CheckReferences() []RefWarning {
	if r == nil || r.State == nil || len(r.Questions) == 0 {
		return nil
	}

	// Resolution walks map[string]any and []any, so normalize whatever the
	// caller passed — a struct, a map, a slice — through JSON first. That also
	// means the paths are checked against the bytes actually sent, including
	// any effect of json tags, rather than against the Go field names.
	state, ok := normalize(r.State)
	if !ok {
		return nil
	}

	var out []RefWarning
	ids := make([]string, 0, len(r.Questions))
	for id := range r.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		q, ok := normalize(r.Questions[id])
		if !ok {
			continue
		}
		for _, ref := range statepath.Check(state, q) {
			out = append(out, RefWarning{QuestionID: id, Path: ref.Path, Reason: ref.Reason})
		}
	}
	return out
}

// normalize converts any value to the generic JSON shape, matching what the
// server will receive.
func normalize(v any) (any, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false
	}
	return out, true
}
