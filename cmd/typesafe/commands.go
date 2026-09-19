package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/cassette"
	"github.com/nibir1/typesafe-go/decision"
)

// --- run ---------------------------------------------------------------------

func cmdRun(ctx context.Context, args []string) int {
	fs := newFlagSet("run [flags]", "Evaluate a request against the API.", `  # Three questions about one ticket, no file needed
  typesafe run --state-text "Payouts have failed for 3 days" \
    --noul urgent="Does this convey urgency?" \
    --choice team="billing,technical,sales" \
    --score severity="Low,Medium,High"

  # From a request file, as JSON
  typesafe run -f request.json --format json

  # From stdin
  cat request.json | typesafe run -f -`)

	var rf requestFlags
	rf.bind(fs)
	format := fs.String("format", "table", "output format: table or json")
	dry := fs.Bool("dry-run", false, "validate and estimate, but send nothing")
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")

	if code, ok := parse(fs, args); !ok {
		return code
	}

	req, err := rf.build()
	if err != nil {
		errorf("%v", err)
		fs.Usage()
		return exitUsage
	}
	if !outputFormat(*format).valid() {
		errorf("unknown format %q: use table or json", *format)
		return exitUsage
	}

	// Validation before the network: the error names the offending question
	// and field, which the server's own 422 does only sometimes.
	if _, err := req.Validate(); err != nil {
		return report(err)
	}
	for _, w := range req.CheckReferences() {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	if *dry {
		fmt.Print(req.EstimateTokens())
		fmt.Println("\ndry run: nothing was sent")
		return exitOK
	}

	client, err := typesafe.NewClient()
	if err != nil {
		return report(err)
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	resp, err := client.SystemOne(ctx, req)
	if err != nil {
		return report(err)
	}

	if outputFormat(*format) == formatJSON {
		if err := printJSON(resp); err != nil {
			return report(err)
		}
		return exitOK
	}
	printAnswers(resp)
	return exitOK
}

// --- estimate ----------------------------------------------------------------

func cmdEstimate(ctx context.Context, args []string) int {
	fs := newFlagSet("estimate [flags]",
		"Estimate a request's token count and cost. Makes no network request.", `  typesafe estimate -f request.json
  typesafe estimate -f request.json --rate 0.02 --count 50000`)

	var rf requestFlags
	rf.bind(fs)
	rate := fs.Float64("rate", 0, "price per million input tokens (default: the published rate)")
	count := fs.Int("count", 1, "how many such requests, for sizing a workload")
	format := fs.String("format", "table", "output format: table or json")

	if code, ok := parse(fs, args); !ok {
		return code
	}

	req, err := rf.build()
	if err != nil {
		errorf("%v", err)
		fs.Usage()
		return exitUsage
	}

	est := req.EstimateTokens()
	cost := req.EstimateCost(*rate)

	if outputFormat(*format) == formatJSON {
		return reportIf(printJSON(map[string]any{
			"tokens":           est,
			"cost_usd":         cost.InputCostUSD,
			"rate_per_million": cost.RatePerMillionUSD,
			"count":            *count,
			"total_cost_usd":   cost.InputCostUSD * float64(*count),
			"approximate":      true,
		}))
	}

	fmt.Print(est)
	fmt.Printf("\n%s\n", cost)
	if *count > 1 {
		fmt.Printf("\n%d requests: ~%d tokens, ~$%.4f\n",
			*count, est.Total**count, cost.InputCostUSD*float64(*count))
	}
	if est.WouldExceedLimits() {
		errorf("%v", est.Err())
		return exitInvalid
	}
	return exitOK
}

// --- lint --------------------------------------------------------------------

func cmdLint(ctx context.Context, args []string) int {
	fs := newFlagSet("lint [flags]",
		"Check a request for problems before sending it. Makes no network request.", `  typesafe lint -f request.json
  typesafe lint -f request.json --strict   # warnings become failures`)

	var rf requestFlags
	rf.bind(fs)
	strict := fs.Bool("strict", false, "exit non-zero on warnings as well as errors")

	if code, ok := parse(fs, args); !ok {
		return code
	}

	req, err := rf.build()
	if err != nil {
		errorf("%v", err)
		fs.Usage()
		return exitUsage
	}

	var problems int

	warnings, err := req.Validate()
	if err != nil {
		fmt.Printf("ERROR   %v\n", err)
		return exitInvalid
	}
	for _, w := range warnings {
		fmt.Printf("WARN    %s\n", w)
		problems++
	}

	// Backticked state references the server never resolves. A typo here is
	// silent at runtime: the model sees a path naming nothing and answers
	// anyway, slightly worse, with nothing in the response to say so.
	for _, w := range req.CheckReferences() {
		fmt.Printf("WARN    %s\n", w)
		problems++
	}

	est := req.EstimateTokens()
	if est.WouldExceedLimits() {
		fmt.Printf("ERROR   %v\n", est.Err())
		return exitInvalid
	}
	// Flag a request approaching a ceiling, since the estimate over-reports
	// and the margin is where surprises live.
	if est.Total > typesafe.MaxContextTokens*8/10 {
		fmt.Printf("WARN    ~%d tokens is over 80%% of the %d limit\n",
			est.Total, typesafe.MaxContextTokens)
		problems++
	}

	if problems == 0 {
		fmt.Printf("ok      %d question(s), ~%d tokens, no problems found\n",
			len(req.Questions), est.Total)
		return exitOK
	}
	fmt.Printf("\n%d warning(s)\n", problems)
	if *strict {
		return exitInvalid
	}
	return exitOK
}

// --- explain -----------------------------------------------------------------

func cmdExplain(ctx context.Context, args []string) int {
	fs := newFlagSet("explain [flags]",
		"Evaluate a decision policy against a set of answers. Makes no network request.", `  # policy.json holds weights and thresholds; answers.json maps question id to probability
  typesafe explain --policy policy.json --answers answers.json

  # Answers inline
  typesafe explain --policy policy.json --answer asks_for_credentials=0.9 --answer generic_greeting=0.7`)

	policyFile := fs.String("policy", "", "policy JSON file (required)")
	answersFile := fs.String("answers", "", "answers JSON file: {\"question_id\": probability}")
	var inline multiFlag
	fs.Var(&inline, "answer", "answer as id=probability (repeatable)")
	format := fs.String("format", "table", "output format: table or json")

	if code, ok := parse(fs, args); !ok {
		return code
	}
	if *policyFile == "" {
		errorf("--policy is required")
		fs.Usage()
		return exitUsage
	}

	raw, err := os.ReadFile(*policyFile)
	if err != nil {
		errorf("reading policy: %v", err)
		return exitError
	}
	policy, err := decision.LoadPolicy(raw)
	if err != nil {
		errorf("%v", err)
		return exitInvalid
	}

	answers := decision.Nouls{}
	if *answersFile != "" {
		b, err := os.ReadFile(*answersFile)
		if err != nil {
			errorf("reading answers: %v", err)
			return exitError
		}
		if err := json.Unmarshal(b, &answers); err != nil {
			errorf("parsing answers: %v", err)
			return exitInvalid
		}
	}
	for _, spec := range inline {
		id, val, err := splitSpec("answer", spec)
		if err != nil {
			errorf("%v", err)
			return exitUsage
		}
		var p float64
		if _, err := fmt.Sscanf(val, "%g", &p); err != nil {
			errorf("--answer %q: %q is not a number", id, val)
			return exitUsage
		}
		answers[id] = p
	}

	if len(answers) == 0 {
		errorf("no answers given: pass --answers FILE or --answer id=probability")
		return exitUsage
	}

	result, err := policy.Evaluate(answers)
	if err != nil {
		errorf("%v", err)
		return exitInvalid
	}

	if outputFormat(*format) == formatJSON {
		return reportIf(printJSON(map[string]any{
			"policy":  result.Policy,
			"verdict": result.Verdict,
			"score":   result.Score,
			"terms":   result.Trace.Terms,
		}))
	}

	fmt.Printf("policy   %s\n", result.Policy)
	fmt.Printf("verdict  %s\n", result.Verdict)
	fmt.Printf("score    %.4f  %s\n\n", result.Score, bar(result.Score))
	fmt.Println("contributions, largest first:")
	for _, t := range result.Trace.Terms {
		fmt.Printf("  %-28s %.4f x %.4f = %.4f\n", t.QuestionID, t.Weight, t.Value, t.Contribution)
	}
	return exitOK
}

// --- models ------------------------------------------------------------------

func cmdModels(ctx context.Context, args []string) int {
	fs := newFlagSet("models [flags]", "List the models this account may use.",
		"  typesafe models\n  typesafe models --format json")
	format := fs.String("format", "table", "output format: table or json")
	if code, ok := parse(fs, args); !ok {
		return code
	}

	client, err := typesafe.NewClient()
	if err != nil {
		return report(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	models, err := client.Models(ctx)
	if err != nil {
		return report(err)
	}
	if outputFormat(*format) == formatJSON {
		return reportIf(printJSON(models))
	}

	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	fmt.Printf("%-16s %-28s %s\n", "NAME", "RELEASED", "DESCRIPTION")
	for _, m := range models {
		fmt.Printf("%-16s %-28s %s\n", m.Name, m.ReleaseDate, m.Description)
	}
	return exitOK
}

// --- version -----------------------------------------------------------------

func cmdVersion(ctx context.Context, args []string) int {
	fs := newFlagSet("version", "Print version and build information.",
		"  typesafe version\n  typesafe --version")
	if code, ok := parse(fs, args); !ok {
		return code
	}

	fmt.Printf("typesafe %s\n", typesafe.VersionString())

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return exitOK
	}
	fmt.Printf("  go        %s\n", info.GoVersion)
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			fmt.Printf("  revision  %s\n", s.Value)
		case "vcs.time":
			fmt.Printf("  built     %s\n", s.Value)
		case "vcs.modified":
			if s.Value == "true" {
				fmt.Printf("  tree      modified\n")
			}
		case "GOOS", "GOARCH":
			fmt.Printf("  %-9s %s\n", strings.ToLower(s.Key), s.Value)
		}
	}
	return exitOK
}

// --- cassette ----------------------------------------------------------------

func cmdRecord(ctx context.Context, args []string) int {
	fs := newFlagSet("record [flags]", "Capture a live call into a cassette.", `  typesafe record -f request.json --out testdata/cassettes/triage.jsonl

  The Authorization header is never written, and the API key is scrubbed from
  bodies before anything touches disk.`)

	var rf requestFlags
	rf.bind(fs)
	out := fs.String("out", "", "cassette file to write (required)")
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")

	if code, ok := parse(fs, args); !ok {
		return code
	}
	if *out == "" {
		errorf("--out is required")
		fs.Usage()
		return exitUsage
	}
	req, err := rf.build()
	if err != nil {
		errorf("%v", err)
		return exitUsage
	}

	tr := cassette.NewRecord(*out)
	client, err := typesafe.NewClient(typesafe.WithHTTPClient(tr.Client()))
	if err != nil {
		return report(err)
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	resp, callErr := client.SystemOne(ctx, req)
	// Save whatever was captured, including a recorded failure — a cassette of
	// an error is exactly what a test of the error path needs.
	if err := tr.Save(); err != nil {
		errorf("saving cassette: %v", err)
		return exitError
	}
	if callErr != nil {
		fmt.Printf("recorded a failed call to %s\n", *out)
		return report(callErr)
	}

	printAnswers(resp)
	fmt.Printf("\nrecorded to %s\n", *out)
	return exitOK
}

func cmdReplay(ctx context.Context, args []string) int {
	fs := newFlagSet("replay [flags]",
		"Re-run a request against a recorded cassette. Makes no network request.", `  typesafe replay -f request.json --cassette testdata/cassettes/triage.jsonl

  Exits non-zero when the request does not match anything on the tape, which
  usually means the request changed since it was recorded.`)

	var rf requestFlags
	rf.bind(fs)
	tape := fs.String("cassette", "", "cassette file to replay (required)")
	format := fs.String("format", "table", "output format: table or json")

	if code, ok := parse(fs, args); !ok {
		return code
	}
	if *tape == "" {
		errorf("--cassette is required")
		fs.Usage()
		return exitUsage
	}
	req, err := rf.build()
	if err != nil {
		errorf("%v", err)
		return exitUsage
	}

	tr, err := cassette.NewReplay(*tape)
	if err != nil {
		errorf("%v", err)
		return exitError
	}
	// Replay needs no real credential, and must not require one: the whole
	// point is that a cassette works on a machine that has never had a key.
	client, err := typesafe.NewClient(
		typesafe.WithAPIKey("replay"),
		typesafe.WithHTTPClient(tr.Client()),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
	)
	if err != nil {
		return report(err)
	}

	resp, err := client.SystemOne(ctx, req)
	if err != nil {
		// A cassette miss travels back through the HTTP client, which wraps
		// any transport failure as a connection error. Reporting it as one
		// would send the reader looking for a network problem that does not
		// exist, so unwrap it and say what actually happened.
		if errors.Is(err, cassette.ErrNotFound) {
			errorf("this request does not match anything on %s", *tape)
			fmt.Fprintf(os.Stderr, "\n  The recording was made from a different request. Either the\n"+
				"  request changed since, or the cassette is for a different one.\n"+
				"  Re-record with:\n\n    typesafe record -f <request> --out %s\n", *tape)
			return exitMismatch
		}
		errorf("%v", err)
		return exitMismatch
	}

	if outputFormat(*format) == formatJSON {
		return reportIf(printJSON(resp))
	}
	printAnswers(resp)

	if unplayed := tr.Cassette().Unplayed(); len(unplayed) > 0 {
		fmt.Printf("\nnote: %d interaction(s) on this cassette were not used\n", len(unplayed))
	}
	return exitOK
}

func reportIf(err error) int {
	if err != nil {
		return report(err)
	}
	return exitOK
}
