package typesafe

import "encoding/json"

// SystemOneRequest is one evaluation: a single state, and a map of named
// questions asked about it.
//
// All three fields are required by the API. Question ids are yours to choose;
// answers come back under the same ids. The documentation notes that ids are
// not sent to the model and play no part in inference, so name them for your
// own code's benefit and put nothing load-bearing in them.
type SystemOneRequest struct {
	// State is the content every question refers to: a string, or any value
	// that marshals to a JSON object or array.
	//
	// Prefer an object for anything beyond a single passage, so each part of
	// the state has a descriptive name. Jev accepts text only — pre-process
	// images, audio, and binaries into text or structured fields first.
	State any `json:"state"`

	// Model selects which model handles the request. Leave empty to use the
	// client's default (see WithDefaultModel and TYPESAFE_DEFAULT_MODEL).
	//
	// Aliases such as jev-latest move without notice. The response reports
	// the versioned id that actually answered.
	Model string `json:"model"`

	// Questions must contain at least one entry.
	Questions map[string]Question `json:"questions"`
}

// Usage reports token accounting for a request. Only input tokens are billed;
// TypeSafe does not charge for output.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SystemOneResponse is one answer per question, keyed by the ids you sent.
type SystemOneResponse struct {
	// Model is the versioned model id that answered, which may differ from
	// the alias you requested. Log it: aliases move, and a confidence
	// threshold tuned against one version is not calibrated for another.
	Model string `json:"model"`

	// Answers holds one answer per question id.
	//
	// Phase 1 leaves these undecoded. The typed accessors — Noul, Choice,
	// Score — arrive in Phase 2, at which point the raw form remains
	// available for anything this SDK does not model.
	Answers map[string]json.RawMessage `json:"answers"`

	// Usage is the token accounting for this request.
	Usage Usage `json:"usage"`
}

// ModelCard describes one model or alias the account may use.
type ModelCard struct {
	// Name is the id or alias accepted by SystemOneRequest.Model.
	Name string `json:"name"`

	// Description says what the model is for.
	Description string `json:"description"`

	// ReleaseDate is when the model or alias was released.
	ReleaseDate string `json:"release_date"`
}
