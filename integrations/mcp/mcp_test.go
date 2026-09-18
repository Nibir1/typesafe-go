package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/decision"
	tsmcp "github.com/nibir1/typesafe-go/integrations/mcp"
)

// --- harness -----------------------------------------------------------------

func apiServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

func okHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{"name": "jev-latest", "description": "alias", "release_date": "2026-01-01"},
					{"name": "jev-1.13.0", "description": "pinned", "release_date": "2026-01-01"},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-1.13.0",
			"answers": map[string]any{
				"is_spam":    map[string]any{"type": "noul", "noul": 0.81},
				"is_abusive": map[string]any{"type": "noul", "noul": 0.62},
				"team": map[string]any{
					"type": "choice", "choice": "billing", "confidence": 0.77,
					"probabilities": map[string]float64{"billing": 0.77, "technical": 0.23},
				},
				"severity": map[string]any{
					"type": "score", "score": 2.1, "confidence": 0.65,
					"legend":        map[string]any{"0": "none", "1": "minor", "2": "major"},
					"probabilities": map[string]float64{"0": 0.1, "1": 0.2, "2": 0.7},
				},
			},
			"usage": map[string]any{"input_tokens": 240, "output_tokens": 30},
		})
	}
}

func newServer(t *testing.T, h http.HandlerFunc, opts tsmcp.Options) *tsmcp.Server {
	t.Helper()
	client, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(apiServer(t, h)),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return tsmcp.NewServer(client, opts)
}

// connect wires a client to the server over the SDK's in-memory transport and
// performs the initialize handshake.
func connect(t *testing.T, s *tsmcp.Server) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	serverT, clientT := sdkmcp.NewInMemoryTransports()

	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Run(ctx, serverT) }()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		<-serverDone
	})
	return session
}

func callJSON(t *testing.T, res *sdkmcp.CallToolResult, out any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("the tool reported an error: %s", textOf(res))
	}
	if res.StructuredContent == nil {
		t.Fatalf("no structured content; text was: %s", textOf(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal into %T: %v\n%s", out, err, b)
	}
}

func textOf(res *sdkmcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func moderationSpec() tsmcp.PolicySpec {
	return tsmcp.PolicySpec{
		Policy: decision.Policy{
			Name:        "moderation",
			Weights:     decision.Weights{"is_spam": 1, "is_abusive": 2},
			Normalize:   true,
			ReviewAbove: 0.5,
			BlockAbove:  0.9,
		},
		Questions: typesafe.Questions{
			"is_spam":    typesafe.Noul{Instructions: "Is this spam?"},
			"is_abusive": typesafe.Noul{Instructions: "Is this abusive?"},
		},
		Description: "Moderate user-submitted text.",
	}
}

// --- the exit criterion: initialize, tools/list, tools/call ------------------

func TestInitializeAndToolsList(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{})
	if err := s.AddPolicy("moderation", moderationSpec()); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}
	session := connect(t, s)

	// initialize already succeeded inside connect; confirm the server
	// identified itself rather than returning an empty implementation.
	if init := session.InitializeResult(); init == nil || init.ServerInfo.Name != "typesafe" {
		t.Fatalf("initialize returned %+v", init)
	}

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	byName := map[string]*sdkmcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	for _, want := range []string{tsmcp.ToolSystemOne, tsmcp.ToolModels, tsmcp.ToolEvaluatePolicy} {
		tool, ok := byName[want]
		if !ok {
			t.Fatalf("tools/list did not offer %q; got %v", want, keys(byName))
		}
		// A tool with no description is a tool the model will not choose.
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("%s has no description", want)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s has no input schema, so arguments cannot be validated", want)
		}
	}
}

func TestSystemOneToolCall(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{})
	session := connect(t, s)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: tsmcp.ToolSystemOne,
		Arguments: map[string]any{
			"state": "Buy cheap watches now!!!",
			"questions": map[string]any{
				"is_spam": map[string]any{"type": "noul", "instructions": "Is this spam?"},
				"team": map[string]any{
					"type": "choice", "instructions": "Which team?",
					"options": map[string]string{"billing": "money", "technical": "bugs"},
				},
				"severity": map[string]any{
					"type": "score", "instructions": "How bad?",
					"levels": []string{"none", "minor", "major"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}

	var out tsmcp.SystemOneResult
	callJSON(t, res, &out)

	if out.Model != "jev-1.13.0" {
		t.Errorf("model = %q", out.Model)
	}
	if out.InputTokens != 240 {
		t.Errorf("input_tokens = %d", out.InputTokens)
	}

	// A Noul carries a probability and no confidence. Reporting a zero
	// confidence would read to an agent as "completely unsure".
	spam := out.Answers["is_spam"]
	if spam.Probability == nil || *spam.Probability != 0.81 {
		t.Errorf("is_spam probability = %v", spam.Probability)
	}
	if spam.Confidence != nil {
		t.Errorf("a Noul reported confidence %v; it has none", *spam.Confidence)
	}

	team := out.Answers["team"]
	if team.Choice != "billing" || team.Confidence == nil {
		t.Errorf("team = %+v", team)
	}
	if len(team.Distribution) != 2 {
		t.Errorf("team distribution = %v", team.Distribution)
	}

	sev := out.Answers["severity"]
	if sev.Score == nil || *sev.Score != 2.1 {
		t.Errorf("severity score = %v", sev.Score)
	}
}

func TestModelsToolCall(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{})
	session := connect(t, s)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: tsmcp.ToolModels})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}

	var out tsmcp.ModelsResult
	callJSON(t, res, &out)
	if len(out.Models) != 2 {
		t.Fatalf("%d models, want 2", len(out.Models))
	}
	if out.Models[0].Name != "jev-latest" {
		t.Errorf("first model = %q", out.Models[0].Name)
	}
}

// --- the differentiator ------------------------------------------------------

// The agent names a policy and supplies text. It never sees the questions, the
// weights or the thresholds, and cannot alter them.
func TestEvaluatePolicy(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{})
	if err := s.AddPolicy("moderation", moderationSpec()); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}
	session := connect(t, s)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      tsmcp.ToolEvaluatePolicy,
		Arguments: map[string]any{"policy": "moderation", "state": "you are an idiot"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}

	var out tsmcp.PolicyResult
	callJSON(t, res, &out)

	if out.Policy != "moderation" {
		t.Errorf("policy = %q", out.Policy)
	}
	// weights 1 and 2, values 0.81 and 0.62, normalized: (0.81 + 1.24) / 3
	want := (0.81*1 + 0.62*2) / 3
	if diff := out.Score - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("score = %v, want %v", out.Score, want)
	}
	if out.Verdict != "review" {
		t.Errorf("verdict = %q, want review (score %.4f is above 0.5, below 0.9)", out.Verdict, out.Score)
	}

	// The arithmetic must come back, or the verdict is an assertion.
	if len(out.Contributions) != 2 {
		t.Fatalf("%d contributions, want 2", len(out.Contributions))
	}
	if out.Contributions[0].Question != "is_abusive" {
		t.Errorf("contributions are not ordered by size: %+v", out.Contributions)
	}
	for _, c := range out.Contributions {
		if c.Contribution != c.Weight*c.Value {
			t.Errorf("%s: %v * %v != %v", c.Question, c.Weight, c.Value, c.Contribution)
		}
	}
}

// An unknown policy must fail as a *tool* error the agent can read, not as a
// protocol error, which a client treats as a broken server.
func TestUnknownPolicyIsAToolError(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{})
	if err := s.AddPolicy("moderation", moderationSpec()); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}
	session := connect(t, s)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      tsmcp.ToolEvaluatePolicy,
		Arguments: map[string]any{"policy": "nope", "state": "x"},
	})
	if err != nil {
		t.Fatalf("the protocol call itself should succeed: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unknown policy should be reported as a tool error")
	}
	msg := textOf(res)
	if !strings.Contains(msg, "moderation") {
		t.Errorf("the error should name the policies that do exist, got: %s", msg)
	}
}

// An API failure is a tool error too: a rate limit is not a broken server.
func TestAPIFailureIsAToolError(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": "slow down"})
	}, tsmcp.Options{})
	session := connect(t, s)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: tsmcp.ToolSystemOne,
		Arguments: map[string]any{
			"state":     "x",
			"questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": "?"}},
		},
	})
	if err != nil {
		t.Fatalf("the protocol call itself should succeed: %v", err)
	}
	if !res.IsError {
		t.Fatal("a rate limit should be reported as a tool error")
	}
}

// --- registration guards -----------------------------------------------------

// A policy weighing a question nobody registered fails at registration, where
// an operator can fix it, rather than mid-conversation where an agent cannot.
func TestPolicyMustProvideEveryWeightedQuestion(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{})

	spec := moderationSpec()
	delete(spec.Questions, "is_abusive")

	err := s.AddPolicy("moderation", spec)
	if err == nil {
		t.Fatal("expected registration to fail")
	}
	if !strings.Contains(err.Error(), "is_abusive") {
		t.Errorf("the error should name the missing question, got: %v", err)
	}
}

func TestPolicyRegistrationValidatesThePolicy(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{})

	if err := s.AddPolicy("", moderationSpec()); err == nil {
		t.Error("an unnamed policy should be rejected")
	}
	if err := s.AddPolicy("empty", tsmcp.PolicySpec{
		Policy:    decision.Policy{Name: "empty"},
		Questions: typesafe.Questions{"q": typesafe.Noul{}},
	}); err == nil {
		t.Error("a policy with no weights should be rejected")
	}
}

func TestPoliciesAreListedInOrder(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{})
	for _, name := range []string{"zeta", "alpha", "mu"} {
		spec := moderationSpec()
		spec.Policy.Name = name
		if err := s.AddPolicy(name, spec); err != nil {
			t.Fatalf("AddPolicy(%s): %v", name, err)
		}
	}
	got := s.Policies()
	want := []string{"alpha", "mu", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Policies() = %v, want %v", got, want)
		}
	}
}

// --- options -----------------------------------------------------------------

// A deployment that should only expose its own policies can remove the
// free-form tool.
func TestDisableSystemOne(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{DisableSystemOne: true})
	session := connect(t, s)

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name == tsmcp.ToolSystemOne {
			t.Fatal("systemone was offered despite being disabled")
		}
	}
	if len(res.Tools) != 2 {
		t.Errorf("%d tools, want 2", len(res.Tools))
	}
}

func TestMalformedQuestionIsAToolError(t *testing.T) {
	s := newServer(t, okHandler(t), tsmcp.Options{})
	session := connect(t, s)

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"no questions", map[string]any{"state": "x", "questions": map[string]any{}}},
		{"choice with no options", map[string]any{
			"state":     "x",
			"questions": map[string]any{"q": map[string]any{"type": "choice", "instructions": "?"}},
		}},
		{"score with no levels", map[string]any{
			"state":     "x",
			"questions": map[string]any{"q": map[string]any{"type": "score", "instructions": "?"}},
		}},
		{"unknown type", map[string]any{
			"state":     "x",
			"questions": map[string]any{"q": map[string]any{"type": "vibes", "instructions": "?"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
				Name: tsmcp.ToolSystemOne, Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("the protocol call should succeed: %v", err)
			}
			if !res.IsError {
				t.Error("expected a tool error")
			}
		})
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
