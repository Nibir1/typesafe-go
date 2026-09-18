package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	typesafe "github.com/nibir1/typesafe-go"
)

// classify maps an error onto an exit code, so a script can branch without
// parsing stderr. This is the whole reason the codes are distinct.
func classify(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, typesafe.ErrNoAPIKey), errors.Is(err, typesafe.ErrAuthentication),
		errors.Is(err, typesafe.ErrPermissionDenied):
		return exitAuth
	case errors.Is(err, typesafe.ErrConnection), errors.Is(err, typesafe.ErrTimeout):
		return exitNetwork
	case errors.Is(err, typesafe.ErrRateLimit), errors.Is(err, typesafe.ErrOverloaded),
		errors.Is(err, typesafe.ErrBudgetExceeded):
		return exitRateLimited
	case isUnknownModel(err):
		return exitUnknownModel
	case errors.Is(err, typesafe.ErrInvalidRequest), errors.Is(err, typesafe.ErrBadRequest):
		return exitInvalid
	default:
		return exitError
	}
}

// isUnknownModel recognizes the 400 the API returns for a model name it does
// not know. There is no distinct status for it, so the message is the signal —
// matched narrowly, and only within an api_usage_error.
func isUnknownModel(err error) bool {
	var api *typesafe.APIError
	if !errors.As(err, &api) || api.Reason == nil {
		return false
	}
	return api.Reason.ErrorType == "api_usage_error" &&
		strings.Contains(strings.ToLower(api.Reason.Message), "unknown model")
}

// report prints an error and returns its exit code.
func report(err error) int {
	code := classify(err)
	errorf("%v", err)

	// A bare failure leaves the reader guessing at the next step. Where the
	// cause is known, say what to do about it.
	switch code {
	case exitAuth:
		fmt.Fprintln(os.Stderr, "\n  Set TYPESAFE_API_KEY, or check that the key is still valid.")
	case exitNetwork:
		fmt.Fprintln(os.Stderr, "\n  Could not reach the API. Run 'typesafe doctor' to narrow it down.")
	case exitRateLimited:
		fmt.Fprintln(os.Stderr, "\n  Rate limited or over budget. Wait and retry.")
	case exitUnknownModel:
		fmt.Fprintln(os.Stderr, "\n  Run 'typesafe models' to see what this account may use.")
	}
	return code
}

// outputFormat selects how a result is rendered.
type outputFormat string

const (
	formatTable outputFormat = "table"
	formatJSON  outputFormat = "json"
)

func (f outputFormat) valid() bool { return f == formatTable || f == formatJSON }

// printJSON writes v as indented JSON.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// uncertaintyBand is how close to 0.5 a Noul probability must be before the
// display calls it out as genuine uncertainty rather than a weak opinion.
//
// Named because it is a judgment about what to tell the reader, not an
// incidental number — which is exactly what confidencecheck flags.
const uncertaintyBand = 0.1

// printAnswers renders a response as a readable table.
//
// Each primitive gets the shape that suits it: a Noul is one number, a Choice
// is a ranked distribution, a Score is a rubric with the mass shown per level.
// Printing all three the same way would hide what distinguishes them.
func printAnswers(resp *typesafe.SystemOneResponse) {
	ids := make([]string, 0, len(resp.Answers))
	for id := range resp.Answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		ans, err := resp.Answer(id)
		if err != nil {
			fmt.Printf("%s\n  (could not decode: %v)\n\n", id, err)
			continue
		}
		switch a := ans.(type) {
		case typesafe.NoulAnswer:
			fmt.Printf("%s  [noul]\n  %.4f  %s\n", id, a.Noul, bar(a.Noul))
			if a.Uncertain(uncertaintyBand) {
				fmt.Printf("  near 0.5 — the model is expressing genuine uncertainty\n")
			}
			fmt.Println()

		case typesafe.ChoiceAnswer:
			fmt.Printf("%s  [choice]  -> %s   confidence %.4f\n", id, a.Choice, a.Confidence)
			for _, r := range a.Ranked() {
				marker := " "
				if r.Option == a.Choice {
					marker = "*"
				}
				fmt.Printf("  %s %-24s %.4f  %s\n", marker, r.Option, r.Probability, bar(r.Probability))
			}
			fmt.Printf("  margin over runner-up: %.4f\n\n", a.Margin())

		case typesafe.ScoreAnswer:
			level, label := a.Nearest()
			fmt.Printf("%s  [score]  %.4f -> level %d (%v)   confidence %.4f\n",
				id, a.Score, level, label, a.Confidence)
			probs := a.LevelProbabilities()
			for i, lv := range a.Levels() {
				marker := " "
				if i == level {
					marker = "*"
				}
				fmt.Printf("  %s %d %-22v %.4f  %s\n", marker, i, lv, probs[i], bar(probs[i]))
			}
			fmt.Println()
		}
	}

	fmt.Printf("model %s   %d input tokens, %d output\n",
		resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

// bar renders a probability as a short meter, so a distribution can be read at
// a glance rather than by comparing decimals.
func bar(p float64) string {
	const width = 24
	n := int(p*width + 0.5)
	if n < 0 {
		n = 0
	}
	if n > width {
		n = width
	}
	return strings.Repeat("#", n) + strings.Repeat(".", width-n)
}
