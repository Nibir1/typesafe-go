// Command typesafe-mcp serves TypeSafe over MCP on stdio.
//
//	go install github.com/nibir1/typesafe-go/integrations/mcp/cmd/typesafe-mcp@latest
//
// Point an MCP client at it. For Claude Desktop, in claude_desktop_config.json:
//
//	{
//	  "mcpServers": {
//	    "typesafe": {
//	      "command": "typesafe-mcp",
//	      "env": { "TYPESAFE_API_KEY": "..." }
//	    }
//	  }
//	}
//
// # Policies
//
//	typesafe-mcp -policies ./policies
//
// Every .json file in the directory is loaded as a named policy, taking its
// name from the file. A policy file pairs a decision policy with the questions
// that feed it:
//
//	{
//	  "policy": {
//	    "name": "moderation",
//	    "weights": {"is_spam": 1, "is_abusive": 2},
//	    "normalize": true,
//	    "review_above": 0.5,
//	    "block_above": 0.9
//	  },
//	  "questions": {
//	    "is_spam":    {"type": "noul", "instructions": "Is this spam?"},
//	    "is_abusive": {"type": "noul", "instructions": "Is this abusive?"}
//	  },
//	  "description": "Moderate user-submitted text."
//	}
//
// With -only-policies, the free-form systemone tool is not offered and the
// server exposes nothing but the policies you defined. That is the shape to
// deploy when the agent should not be able to ask arbitrary questions.
//
// # Logging goes to stderr
//
// stdout carries the protocol. Anything written there corrupts the session, so
// every diagnostic goes to stderr — which is also why this command prints
// nothing on a successful start.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/decision"
	tsmcp "github.com/nibir1/typesafe-go/integrations/mcp"
)

// Exit codes, matching the typesafe CLI's convention.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
	exitNoKey   = 3
)

// policyFile is the on-disk form of a policy.
type policyFile struct {
	Policy      decision.Policy            `json:"policy"`
	Questions   map[string]json.RawMessage `json:"questions"`
	Description string                     `json:"description"`
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs := flag.NewFlagSet("typesafe-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		policyDir    = fs.String("policies", "", "directory of policy .json files")
		onlyPolicies = fs.Bool("only-policies", false, "do not offer the free-form systemone tool")
		model        = fs.String("model", "", "pin a model for every call")
		showVersion  = fs.Bool("version", false, "print the version and exit")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *showVersion {
		fmt.Fprintf(os.Stdout, "typesafe-mcp %s\n", typesafe.VersionString())
		return exitOK
	}

	if os.Getenv(typesafe.EnvAPIKey) == "" {
		fmt.Fprintf(os.Stderr,
			"typesafe-mcp: %s is not set.\nExport a key from the TypeSafe console:\n  export %s=...\n",
			typesafe.EnvAPIKey, typesafe.EnvAPIKey)
		return exitNoKey
	}

	client, err := typesafe.NewClient(typesafe.WithUserAgentSuffix("typesafe-mcp"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "typesafe-mcp: %v\n", err)
		return exitFailure
	}

	srv := tsmcp.NewServer(client, tsmcp.Options{
		DefaultModel:     *model,
		DisableSystemOne: *onlyPolicies,
	})

	if *policyDir != "" {
		loaded, err := loadPolicies(srv, *policyDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "typesafe-mcp: %v\n", err)
			return exitFailure
		}
		fmt.Fprintf(os.Stderr, "typesafe-mcp: loaded %d policy file(s): %s\n",
			loaded, strings.Join(srv.Policies(), ", "))
	} else if *onlyPolicies {
		fmt.Fprint(os.Stderr,
			"typesafe-mcp: -only-policies with no -policies directory leaves nothing "+
				"but the models tool.\n")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx, &sdkmcp.StdioTransport{}); err != nil {
		if ctx.Err() != nil {
			return exitOK // a clean shutdown, not a failure
		}
		fmt.Fprintf(os.Stderr, "typesafe-mcp: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// loadPolicies registers every .json file in dir, named after the file.
func loadPolicies(srv *tsmcp.Server, dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // deterministic load order, so errors are reproducible

	if len(names) == 0 {
		return 0, fmt.Errorf("no .json policy files in %s", dir)
	}

	for _, name := range names {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			return 0, fmt.Errorf("reading %s: %w", path, err)
		}

		var pf policyFile
		if err := json.Unmarshal(b, &pf); err != nil {
			return 0, fmt.Errorf("parsing %s: %w", path, err)
		}

		questions := make(typesafe.Questions, len(pf.Questions))
		for id, raw := range pf.Questions {
			var q typesafe.RawQuestion
			if err := json.Unmarshal(raw, &q); err != nil {
				return 0, fmt.Errorf("%s: question %q: %w", path, id, err)
			}
			questions[id] = q
		}

		policyName := strings.TrimSuffix(name, ".json")
		if pf.Policy.Name == "" {
			pf.Policy.Name = policyName
		}
		if err := srv.AddPolicy(policyName, tsmcp.PolicySpec{
			Policy:      pf.Policy,
			Questions:   questions,
			Description: pf.Description,
		}); err != nil {
			return 0, fmt.Errorf("%s: %w", path, err)
		}
	}
	return len(names), nil
}
