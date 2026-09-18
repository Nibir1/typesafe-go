package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
)

// cmdDoctor diagnoses a setup, one layer at a time.
//
// The value is in separating the causes. "It does not work" covers a missing
// environment variable, a revoked key, a proxy eating TLS, a model name that
// was renamed, and an account at its rate limit — and the fix for each is
// different. Each check runs only if the one below it passed, so the first
// failure is the real one rather than a cascade.
//
// The exit code names the cause, so this is usable from a script.
func cmdDoctor(ctx context.Context, args []string) int {
	fs := newFlagSet("doctor [flags]", "Diagnose configuration and connectivity.", `  typesafe doctor
  typesafe doctor --model jev-1.13.0

  Exit codes: 0 ok, 3 credential, 4 network, 6 rate limited, 7 unknown model.`)
	model := fs.String("model", "", "also check that this model name resolves")
	timeout := fs.Duration("timeout", 30*time.Second, "per-check timeout")
	if code, ok := parse(fs, args); !ok {
		return code
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	fmt.Println("typesafe doctor")
	fmt.Println()

	// 1. Configuration, before anything touches the network.
	key := os.Getenv(typesafe.EnvAPIKey)
	if strings.TrimSpace(key) == "" {
		fail("API key", fmt.Sprintf("%s is not set", typesafe.EnvAPIKey))
		hint("Export a key from the TypeSafe console:\n" +
			"    export " + typesafe.EnvAPIKey + "=...")
		return exitAuth
	}
	// Never print the key, not even a prefix. A key fragment in a terminal is
	// a key fragment in a screenshot, a scrollback buffer and a bug report.
	ok("API key", fmt.Sprintf("%s is set (%d characters)", typesafe.EnvAPIKey, len(key)))

	baseURL := os.Getenv(typesafe.EnvBaseURL)
	if baseURL == "" {
		baseURL = typesafe.DefaultBaseURL
		ok("base URL", baseURL+" (default)")
	} else {
		ok("base URL", baseURL+" (from "+typesafe.EnvBaseURL+")")
	}

	defaultModel := os.Getenv(typesafe.EnvDefaultModel)
	if defaultModel == "" {
		defaultModel = typesafe.DefaultModel
		ok("model", defaultModel+" (default)")
	} else {
		ok("model", defaultModel+" (from "+typesafe.EnvDefaultModel+")")
	}

	client, err := typesafe.NewClient(typesafe.WithRetryPolicy(typesafe.NoRetry()))
	if err != nil {
		fail("client", err.Error())
		return classify(err)
	}

	// 2. Reachability and credential, in one call. GET /v1/models is the
	//    cheapest request that exercises both.
	models, err := client.Models(ctx)
	if err != nil {
		switch classify(err) {
		case exitAuth:
			fail("connectivity", "the API rejected the credential")
			hint("The key is set but not accepted. It may be revoked, from another\n" +
				"    environment, or copied with surrounding whitespace.")
		case exitNetwork:
			fail("connectivity", err.Error())
			hint("Could not reach " + baseURL + ". Check network access, a proxy, or\n" +
				"    a firewall intercepting TLS.")
		case exitRateLimited:
			fail("connectivity", "rate limited")
			hint("The credential works; the account is at its limit. Retry shortly.")
		default:
			fail("connectivity", err.Error())
		}
		return classify(err)
	}
	ok("connectivity", fmt.Sprintf("reached %s", baseURL))
	ok("credential", "accepted")

	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Name)
	}
	ok("models", fmt.Sprintf("%d available: %s", len(models), strings.Join(names, ", ")))

	// 3. Does the model this setup will actually use resolve? An alias that
	//    was renamed fails every request with a 400 that says only "unknown
	//    model", which is easy to mistake for a broken key.
	target := *model
	if target == "" {
		target = defaultModel
	}
	probe := &typesafe.SystemOneRequest{
		State: "ping",
		Model: target,
		Questions: map[string]typesafe.Question{
			"reachable": typesafe.Noul{Instructions: "Is this a test message?"},
		},
	}
	resp, err := client.SystemOne(ctx, probe)
	if err != nil {
		if classify(err) == exitUnknownModel {
			fail("model "+target, "not recognized by the API")
			hint("Available: " + strings.Join(names, ", ") +
				"\n    Versioned ids such as jev-1.13.0 are also accepted.")
			return exitUnknownModel
		}
		fail("evaluation", err.Error())
		return classify(err)
	}
	ok("model "+target, "resolved to "+resp.Model)
	ok("evaluation", fmt.Sprintf("round trip succeeded, %d input tokens", resp.Usage.InputTokens))

	fmt.Println()
	fmt.Println("Everything checks out.")
	return exitOK
}

func ok(label, detail string)   { fmt.Printf("  ok    %-14s %s\n", label, detail) }
func fail(label, detail string) { fmt.Printf("  FAIL  %-14s %s\n", label, detail) }
func hint(s string)             { fmt.Printf("\n    %s\n", s) }
