// Command typesafe is a command-line client for the TypeSafe System One API.
//
//	typesafe run       evaluate a request
//	typesafe estimate  token and cost pre-flight, no network
//	typesafe lint      check a request for problems, no network
//	typesafe explain   evaluate a decision policy, no network
//	typesafe models    list available models
//	typesafe doctor    diagnose configuration and connectivity
//	typesafe replay    re-run a recorded cassette offline
//	typesafe record    capture a cassette from a live call
//	typesafe version   version and build information
//
// Run `typesafe <command> -h` for the flags and worked examples of any one.
//
// # No dependencies
//
// The roadmap permitted a flag-parsing library here on the grounds that the CLI
// would be a separate module. It is not one, and it does not need the library.
// Keeping it inside the main module means
// `go install github.com/nibir1/typesafe-go/cmd/typesafe@latest` resolves with
// no nested-module version tagging, and `make deps` proves the whole
// repository — binary included — has no third-party code in it.
//
// The cost is stdlib flag's subcommand handling, which is a dispatch table and
// about forty lines. That is a good trade.
//
// # What is deliberately absent
//
// YAML input and output would require a dependency for a format JSON already
// covers. The roadmap listed both; this ships JSON and a human-readable table.
//
// `codegen` generates typed question definitions from Go structs, which needs
// the generic question types that arrive in a later phase. Adding it now would
// mean generating code against an API that does not exist yet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// Exit codes. Distinct values so a script can branch on the failure without
// parsing stderr — `doctor` in particular exists to be read by a machine.
const (
	exitOK           = 0
	exitError        = 1 // generic failure
	exitUsage        = 2 // bad flags or arguments
	exitAuth         = 3 // missing or rejected credential
	exitNetwork      = 4 // could not reach the API
	exitInvalid      = 5 // the request would be rejected
	exitRateLimited  = 6 // rate limited or over budget
	exitUnknownModel = 7 // the model name is not recognized
	exitMismatch     = 8 // replay differed from the recording
)

// command is one subcommand.
type command struct {
	name    string
	summary string
	// offline reports whether the command needs neither network nor a key.
	offline bool
	run     func(ctx context.Context, args []string) int
}

func commands() []command {
	return []command{
		{"run", "Evaluate a request against the API", false, cmdRun},
		{"estimate", "Estimate tokens and cost without sending anything", true, cmdEstimate},
		{"lint", "Check a request for problems before sending it", true, cmdLint},
		{"explain", "Evaluate a decision policy against a set of answers", true, cmdExplain},
		{"models", "List the models this account may use", false, cmdModels},
		{"doctor", "Diagnose configuration and connectivity", false, cmdDoctor},
		{"replay", "Re-run a recorded cassette with no network", true, cmdReplay},
		{"record", "Capture a live call into a cassette", false, cmdRecord},
		{"completion", "Print a shell completion script", true, cmdCompletion},
		{"version", "Print version and build information", true, cmdVersion},
	}
}

func main() {
	// Ctrl-C cancels in flight rather than killing mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:]))
}

func run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		usage(os.Stdout)
		return exitUsage
	}

	switch args[0] {
	case "-h", "--help", "help":
		usage(os.Stdout)
		return exitOK
	case "-v", "--version":
		return cmdVersion(ctx, nil)
	}

	for _, c := range commands() {
		if c.name == args[0] {
			return c.run(ctx, args[1:])
		}
	}

	fmt.Fprintf(os.Stderr, "typesafe: unknown command %q\n\n", args[0])
	if s := nearest(args[0]); s != "" {
		fmt.Fprintf(os.Stderr, "Did you mean %q?\n\n", s)
	}
	usage(os.Stderr)
	return exitUsage
}

func usage(w *os.File) {
	fmt.Fprint(w, `typesafe — a command-line client for the TypeSafe System One API

USAGE
  typesafe <command> [flags]

COMMANDS
`)
	for _, c := range commands() {
		marker := " "
		if c.offline {
			marker = "*"
		}
		fmt.Fprintf(w, "  %-11s %s %s\n", c.name, marker, c.summary)
	}
	fmt.Fprint(w, `
  * needs no API key and makes no network request

EXAMPLES
  # Ask three questions about a ticket
  typesafe run --state-text "Payouts failing for 3 days" \
    --noul urgent="Does this convey urgency?" \
    --choice team="billing,technical,sales" \
    --score severity="Low,Medium,High"

  # Check size and cost before spending anything
  typesafe estimate -f request.json

  # Is my setup working?
  typesafe doctor

CONFIGURATION
  TYPESAFE_API_KEY        required for anything that calls the API
  TYPESAFE_BASE_URL       defaults to https://api.typesafe.ai
  TYPESAFE_DEFAULT_MODEL  defaults to jev-latest

Run 'typesafe <command> -h' for flags and examples.
`)
}

// nearest suggests a command for a likely typo, by edit distance.
func nearest(input string) string {
	best, bestDist := "", 3 // anything further away is not a typo
	for _, c := range commands() {
		if d := editDistance(strings.ToLower(input), c.name); d < bestDist {
			best, bestDist = c.name, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// newFlagSet builds a flag set that prints examples along with the flags.
func newFlagSet(name, synopsis, examples string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "%s\n\nUSAGE\n  typesafe %s\n\nFLAGS\n", synopsis, name)
		fs.PrintDefaults()
		if examples != "" {
			fmt.Fprintf(fs.Output(), "\nEXAMPLES\n%s\n", examples)
		}
	}
	return fs
}

// parse handles the flag set, mapping a parse failure to a usage exit.
func parse(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	return exitOK, true
}

func errorf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "typesafe: "+format+"\n", a...)
}
