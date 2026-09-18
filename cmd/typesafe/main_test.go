package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capture runs the CLI with stdout and stderr redirected, returning both and
// the exit code. The commands write to os.Stdout directly, which is the right
// thing for a CLI and means a test has to redirect rather than inject.
func capture(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	done := make(chan [2]string, 1)
	go func() {
		var o, e strings.Builder
		copyAll(&o, outR)
		copyAll(&e, errR)
		done <- [2]string{o.String(), e.String()}
	}()

	code = run(context.Background(), args)

	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = origOut, origErr
	both := <-done
	return both[0], both[1], code
}

func copyAll(w *strings.Builder, r *os.File) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// offlineOnly clears credentials so a test cannot accidentally reach the API.
// A unit suite that silently starts making live calls is a suite that fails in
// CI for reasons unrelated to the change under test.
func offlineOnly(t *testing.T) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("TYPESAFE_BASE_URL", "http://127.0.0.1:1")
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

const sampleRequest = `{
  "state": "Payouts have been failing for three days.",
  "model": "jev-latest",
  "questions": {
    "urgent": {"type": "noul", "instructions": "Does this convey urgency?"},
    "team": {"type": "choice", "instructions": "Which team?",
             "criteria": {"billing": "Payments", "technical": "Bugs"}}
  }
}`

// --- dispatch ----------------------------------------------------------------

func TestNoArgumentsShowsUsage(t *testing.T) {
	out, _, code := capture(t)
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(out, "COMMANDS") {
		t.Errorf("usage should list commands:\n%s", out)
	}
}

func TestUnknownCommandSuggestsTheNearest(t *testing.T) {
	_, errOut, code := capture(t, "modles")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, `Did you mean "models"`) {
		t.Errorf("a near-miss should be suggested:\n%s", errOut)
	}

	// Something genuinely unrelated should not produce a bogus suggestion.
	_, errOut, _ = capture(t, "xyzzyplugh")
	if strings.Contains(errOut, "Did you mean") {
		t.Errorf("no suggestion should be offered for %q:\n%s", "xyzzyplugh", errOut)
	}
}

// TestEveryCommandHasHelp covers the exit criterion that -h works everywhere.
func TestEveryCommandHasHelp(t *testing.T) {
	for _, c := range commands() {
		t.Run(c.name, func(t *testing.T) {
			out, errOut, code := capture(t, c.name, "-h")
			if code != exitOK {
				t.Errorf("exit = %d, want 0", code)
			}
			text := out + errOut
			if !strings.Contains(text, "USAGE") {
				t.Errorf("help has no USAGE section:\n%s", text)
			}
			// Every command's help must show a worked example: a flag list
			// alone does not tell anyone how to use it.
			if !strings.Contains(text, "EXAMPLES") {
				t.Errorf("help has no EXAMPLES section:\n%s", text)
			}
			if !strings.Contains(text, "typesafe "+strings.Fields(c.name)[0]) {
				t.Errorf("examples should invoke the command:\n%s", text)
			}
		})
	}
}

func TestVersionReportsBuildInfo(t *testing.T) {
	out, _, code := capture(t, "version")
	if code != exitOK {
		t.Errorf("exit = %d", code)
	}
	if !strings.Contains(out, "typesafe") || !strings.Contains(out, "go") {
		t.Errorf("version output = %q", out)
	}
}

// --- offline commands --------------------------------------------------------

func TestEstimateIsOffline(t *testing.T) {
	offlineOnly(t)
	f := writeTemp(t, "req.json", sampleRequest)

	out, _, code := capture(t, "estimate", "-f", f)
	if code != exitOK {
		t.Fatalf("exit = %d, out:\n%s", code, out)
	}
	for _, want := range []string{"approximate", "fixed overhead", "total", "urgent", "team"} {
		if !strings.Contains(out, want) {
			t.Errorf("estimate output missing %q:\n%s", want, out)
		}
	}
}

func TestEstimateJSON(t *testing.T) {
	offlineOnly(t)
	f := writeTemp(t, "req.json", sampleRequest)

	out, _, code := capture(t, "estimate", "-f", f, "--format", "json", "--count", "1000")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got["approximate"] != true {
		t.Error("JSON output must carry the approximate flag")
	}
	if got["count"].(float64) != 1000 {
		t.Errorf("count = %v", got["count"])
	}
}

func TestLintPassesACleanRequest(t *testing.T) {
	offlineOnly(t)
	f := writeTemp(t, "req.json", sampleRequest)

	out, _, code := capture(t, "lint", "-f", f)
	if code != exitOK {
		t.Fatalf("exit = %d, out:\n%s", code, out)
	}
	if !strings.Contains(out, "no problems found") {
		t.Errorf("out = %s", out)
	}
}

// TestLintCatchesAnUnresolvableStateReference is the CLI surface of the check
// no other TypeSafe client has. A backticked path naming nothing is silent at
// runtime: the model answers anyway, slightly worse.
func TestLintCatchesAnUnresolvableStateReference(t *testing.T) {
	offlineOnly(t)
	f := writeTemp(t, "req.json", `{
	  "state": {"ticket": {"messages": [{"text": "refund please"}]}},
	  "model": "jev-latest",
	  "questions": {
	    "refund": {"type": "noul",
	               "instructions": "Does `+"`ticket.messages[0].mesage`"+` request a refund?"}
	  }
	}`)

	out, _, code := capture(t, "lint", "-f", f)
	if code != exitOK {
		t.Fatalf("a warning should not fail without --strict: exit %d", code)
	}
	if !strings.Contains(out, "does not resolve") {
		t.Errorf("the typo should be reported:\n%s", out)
	}
	if !strings.Contains(out, "mesage") {
		t.Errorf("the message should name the bad key:\n%s", out)
	}

	_, _, code = capture(t, "lint", "-f", f, "--strict")
	if code != exitInvalid {
		t.Errorf("--strict exit = %d, want %d", code, exitInvalid)
	}
}

// TestLintRejectsAnOversizeScore: the ceiling is enforced client-side, so the
// error arrives without spending a round trip on a 400.
func TestLintRejectsAnOversizeScore(t *testing.T) {
	offlineOnly(t)
	levels := make([]string, 11)
	for i := range levels {
		levels[i] = `"level"`
	}
	f := writeTemp(t, "req.json", `{"state":"x","model":"jev-latest","questions":{
	  "sev":{"type":"score","instructions":"Rate","criteria":[`+strings.Join(levels, ",")+`]}}}`)

	out, _, code := capture(t, "lint", "-f", f)
	if code != exitInvalid {
		t.Fatalf("exit = %d, want %d\n%s", code, exitInvalid, out)
	}
	if !strings.Contains(out, "at most 10") {
		t.Errorf("the error should state the limit:\n%s", out)
	}
	// And name the offending question, which the server's 400 does not.
	if !strings.Contains(out, "sev") {
		t.Errorf("the error should name the question:\n%s", out)
	}
}

func TestExplainEvaluatesAPolicy(t *testing.T) {
	offlineOnly(t)
	policy := writeTemp(t, "policy.json", `{"name":"spam-v1","normalize":true,
	  "weights":{"creds":0.6,"urgency":0.4},"block_above":0.8}`)

	out, _, code := capture(t, "explain", "--policy", policy,
		"--answer", "creds=0.95", "--answer", "urgency=0.9")
	if code != exitOK {
		t.Fatalf("exit = %d, out:\n%s", code, out)
	}
	for _, want := range []string{"spam-v1", "block", "creds", "contributions"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q:\n%s", want, out)
		}
	}
}

func TestExplainRejectsAnUnreachableThreshold(t *testing.T) {
	offlineOnly(t)
	policy := writeTemp(t, "policy.json",
		`{"name":"bad","weights":{"a":1},"review_above":0.9,"block_above":0.2}`)

	_, errOut, code := capture(t, "explain", "--policy", policy, "--answer", "a=0.5")
	if code != exitInvalid {
		t.Errorf("exit = %d, want %d", code, exitInvalid)
	}
	if !strings.Contains(errOut, "unreachable") {
		t.Errorf("err = %s", errOut)
	}
}

// --- argument handling -------------------------------------------------------

func TestRequestFlagErrors(t *testing.T) {
	offlineOnly(t)
	cases := map[string][]string{
		"no request at all":      {"lint"},
		"file and inline both":   {"lint", "-f", "x.json", "--noul", "a=b"},
		"inline with no state":   {"lint", "--noul", "a=b"},
		"malformed question":     {"lint", "--state-text", "x", "--noul", "no-equals-sign"},
		"choice with one option": {"lint", "--state-text", "x", "--choice", "a=only"},
		"missing file":           {"lint", "-f", "/nonexistent/request.json"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, errOut, code := capture(t, args...)
			if code == exitOK {
				t.Fatalf("expected a non-zero exit\n%s", errOut)
			}
			if !strings.Contains(errOut, "typesafe:") {
				t.Errorf("error should be prefixed and explain itself:\n%s", errOut)
			}
		})
	}
}

func TestInlineQuestionsBuildARequest(t *testing.T) {
	offlineOnly(t)
	out, _, code := capture(t, "estimate",
		"--state-text", "a ticket",
		"--noul", "urgent=Is this urgent?",
		"--choice", "team=billing,technical",
		"--score", "sev=Low,High")
	if code != exitOK {
		t.Fatalf("exit = %d, out:\n%s", code, out)
	}
	for _, want := range []string{"urgent", "team", "sev"} {
		if !strings.Contains(out, want) {
			t.Errorf("estimate should list %q:\n%s", want, out)
		}
	}
}

func TestStateFileAcceptsJSONAndText(t *testing.T) {
	offlineOnly(t)

	jsonState := writeTemp(t, "state.json", `{"ticket":{"subject":"hi"}}`)
	_, _, code := capture(t, "estimate", "--state", jsonState, "--noul", "q=Is it?")
	if code != exitOK {
		t.Errorf("a JSON state file should work: exit %d", code)
	}

	textState := writeTemp(t, "state.txt", "just some prose about a ticket")
	_, _, code = capture(t, "estimate", "--state", textState, "--noul", "q=Is it?")
	if code != exitOK {
		t.Errorf("a plain-text state file should work: exit %d", code)
	}

	broken := writeTemp(t, "broken.json", `{"unterminated":`)
	_, errOut, code := capture(t, "estimate", "--state", broken, "--noul", "q=Is it?")
	if code == exitOK {
		t.Error("a file that looks like JSON but does not parse should fail")
	}
	if !strings.Contains(errOut, "does not parse") {
		t.Errorf("err = %s", errOut)
	}
}

// --- credentials -------------------------------------------------------------

// TestNoCommandPrintsTheKey is the one failure here that cannot be undone: a
// key in terminal output is a key in a screenshot, a scrollback buffer, and a
// pasted bug report.
func TestNoCommandPrintsTheKey(t *testing.T) {
	const key = "sk-live-SECRET-0123456789abcdef"
	t.Setenv("TYPESAFE_API_KEY", key)
	t.Setenv("TYPESAFE_BASE_URL", "http://127.0.0.1:1") // refuses instantly

	f := writeTemp(t, "req.json", sampleRequest)
	for _, args := range [][]string{
		{"version"}, {"help"}, {"completion", "bash"},
		{"estimate", "-f", f}, {"lint", "-f", f},
		{"doctor"}, {"models"}, {"run", "-f", f},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			out, errOut, _ := capture(t, args...)
			if strings.Contains(out+errOut, key) {
				t.Errorf("the API key appeared in output:\n%s%s", out, errOut)
			}
		})
	}
}

// TestDoctorDistinguishesFailureModes covers the exit criterion. Only the
// offline cases are asserted here; the live ones need a key and belong in the
// integration suite.
func TestDoctorDistinguishesFailureModes(t *testing.T) {
	t.Run("no key", func(t *testing.T) {
		t.Setenv("TYPESAFE_API_KEY", "")
		out, _, code := capture(t, "doctor")
		if code != exitAuth {
			t.Errorf("exit = %d, want %d", code, exitAuth)
		}
		if !strings.Contains(out, "FAIL") || !strings.Contains(out, "TYPESAFE_API_KEY") {
			t.Errorf("out = %s", out)
		}
	})

	t.Run("unreachable host", func(t *testing.T) {
		t.Setenv("TYPESAFE_API_KEY", "sk-anything-at-all-here")
		t.Setenv("TYPESAFE_BASE_URL", "http://127.0.0.1:1")
		out, _, code := capture(t, "doctor")
		if code != exitNetwork {
			t.Errorf("exit = %d, want %d\n%s", code, exitNetwork, out)
		}
		// The configuration checks above it must still have passed, so the
		// reader can see how far it got.
		if !strings.Contains(out, "ok    API key") {
			t.Errorf("earlier checks should still report:\n%s", out)
		}
	})
}

// --- completion --------------------------------------------------------------

func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			out, _, code := capture(t, "completion", shell)
			if code != exitOK {
				t.Fatalf("exit = %d", code)
			}
			// Every command must appear, or completion silently lags the CLI.
			for _, c := range commands() {
				if !strings.Contains(out, c.name) {
					t.Errorf("%s completion omits %q", shell, c.name)
				}
			}
		})
	}

	_, errOut, code := capture(t, "completion", "tcsh")
	if code != exitUsage {
		t.Errorf("an unsupported shell should exit %d, got %d", exitUsage, code)
	}
	if !strings.Contains(errOut, "bash") {
		t.Errorf("the error should list the supported shells:\n%s", errOut)
	}

	if _, _, code := capture(t, "completion"); code != exitUsage {
		t.Errorf("completion with no shell should exit %d, got %d", exitUsage, code)
	}
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"models", "models", 0},
		{"modles", "models", 2},
		{"run", "run", 0},
		{"", "run", 3},
		{"doctor", "docter", 1},
	}
	for _, tc := range cases {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
