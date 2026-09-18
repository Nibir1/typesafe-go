package cassette_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/cassette"
	"github.com/nibir1/typesafe-go/typesafetest"
)

const recordKey = "sk-live-0123456789abcdef-NOTREAL"

func clientFor(t *testing.T, hc *http.Client) *typesafe.Client {
	t.Helper()
	c, err := typesafe.NewClient(
		typesafe.WithAPIKey(recordKey),
		typesafe.WithBaseURL("https://api.typesafe.ai"),
		typesafe.WithHTTPClient(hc),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func mixedRequest() *typesafe.SystemOneRequest {
	return &typesafe.SystemOneRequest{
		State: map[string]any{"ticket": "API returning 500s for 20 minutes."},
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
			"department": typesafe.Choice{
				Instructions: "Which team?",
				Criteria:     typesafe.Options{"billing": "Payments", "technical": "Bugs"},
			},
			"frustration": typesafe.Score{
				Instructions: "How frustrated?",
				Criteria:     typesafe.Levels{"Calm", "Frustrated", "Very angry"},
			},
		},
	}
}

// recordMixed records a three-question call against a stub server standing in
// for the live API, and returns the cassette path.
func recordMixed(t *testing.T, dir string) string {
	t.Helper()
	srv := typesafetest.NewServer(t, typesafetest.Answers(map[string]any{
		"is_urgent":   typesafetest.Noul(0.94),
		"department":  typesafetest.Choice(map[string]float64{"billing": 0.11, "technical": 0.89}),
		"frustration": typesafetest.Score([]string{"Calm", "Frustrated", "Very angry"}, []float64{0.12, 0.34, 0.54}),
	}))

	path := filepath.Join(dir, "mixed.jsonl")
	tr := cassette.NewRecord(path, cassette.WithTransport(redirectTo(srv.URL)))
	if _, err := clientFor(t, tr.Client()).SystemOne(context.Background(), mixedRequest()); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := tr.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	return path
}

// redirectTo sends requests aimed at api.typesafe.ai to a local test server,
// so recordings carry the real path while the traffic stays local.
func redirectTo(base string) http.RoundTripper {
	return typesafetest.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		u := *r.URL
		target, _ := http.NewRequest(r.Method, base+u.Path, r.Body)
		target.Header = r.Header
		return http.DefaultTransport.RoundTrip(target)
	})
}

// TestReplayIsByteIdentical is the core promise: a recorded three-question
// mixed call replays offline and produces the same answers.
func TestReplayIsByteIdentical(t *testing.T) {
	path := recordMixed(t, t.TempDir())

	hc := cassette.Replay(t, path)
	resp, err := clientFor(t, hc).SystemOne(context.Background(), mixedRequest())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	typesafetest.AssertNoulAbove(t, resp, "is_urgent", 0.9)
	typesafetest.AssertChoice(t, resp, "department", "technical")
	typesafetest.AssertScoreBetween(t, resp, "frustration", 1.4, 1.5)
	typesafetest.AssertProbabilitiesSumToOne(t, resp)
	cassette.AssertAllPlayed(t, hc)
}

// TestReplayNeedsNoNetwork: the whole point. A replaying transport must never
// reach the network, even if one is available.
func TestReplayNeedsNoNetwork(t *testing.T) {
	path := recordMixed(t, t.TempDir())

	tr, err := cassette.NewReplay(path, cassette.WithTransport(
		typesafetest.RoundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Error("replay reached the underlying transport")
			return nil, errors.New("must not be called")
		})))
	if err != nil {
		t.Fatalf("NewReplay: %v", err)
	}
	if _, err := clientFor(t, tr.Client()).SystemOne(context.Background(), mixedRequest()); err != nil {
		t.Fatalf("replay: %v", err)
	}
}

// TestRecordingIsDeterministic is what makes "re-record and diff" a usable
// drift signal. Two recordings of the same interactions must be byte-identical
// — no timestamps, no durations, no request ids, and no dependence on Go's
// randomized map iteration order.
func TestRecordingIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	first, err := os.ReadFile(recordMixed(t, dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	for i := range 8 {
		second, err := os.ReadFile(recordMixed(t, t.TempDir()))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(first) != string(second) {
			t.Fatalf("recording %d differs from the first:\n first: %s\nsecond: %s", i, first, second)
		}
	}
}

// TestMatchKeyIgnoresKeyOrder: the match key is over the canonical form, so a
// request re-marshalled with different map ordering still finds its recording.
// Without this, replay would be flaky in exactly the way Go maps are.
func TestMatchKeyIgnoresKeyOrder(t *testing.T) {
	path := recordMixed(t, t.TempDir())
	hc := cassette.Replay(t, path)
	c := clientFor(t, hc)

	// Go re-marshals the questions map in a fresh random order each call.
	for i := range 25 {
		if _, err := c.SystemOne(context.Background(), mixedRequest()); err != nil {
			t.Fatalf("call %d missed its recording: %v", i, err)
		}
	}
}

// TestUnknownRequestFailsHelpfully: a miss is the common failure mode here, so
// the message has to say enough to act on.
func TestUnknownRequestFailsHelpfully(t *testing.T) {
	path := recordMixed(t, t.TempDir())
	c := clientFor(t, cassette.Replay(t, path))

	_, err := c.SystemOne(context.Background(), &typesafe.SystemOneRequest{
		State:     "something else entirely",
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{Instructions: "?"}},
	})
	if err == nil {
		t.Fatal("expected a miss")
	}
	if !errors.Is(err, cassette.ErrNotFound) {
		t.Errorf("err = %v, want it to match ErrNotFound", err)
	}
	for _, want := range []string{"POST", "/v1/systemone", "key:", "-update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message should mention %q, got:\n%v", want, err)
		}
	}
}

// TestSecretsNeverReachDisk is the one failure in this package that cannot be
// undone by a later commit.
func TestSecretsNeverReachDisk(t *testing.T) {
	const customerSecret = "CUSTOMER-ACCOUNT-99887766"

	srv := typesafetest.NewServer(t, typesafetest.Answers(map[string]any{
		"q": typesafetest.Noul(0.5),
	}))
	path := filepath.Join(t.TempDir(), "secret.jsonl")

	tr := cassette.NewRecord(path,
		cassette.WithTransport(redirectTo(srv.URL)),
		cassette.WithSecret(customerSecret))

	req := &typesafe.SystemOneRequest{
		State:     map[string]any{"account": customerSecret},
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{Instructions: "?"}},
	}
	if _, err := clientFor(t, tr.Client()).SystemOne(context.Background(), req); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := tr.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(raw)

	if strings.Contains(text, recordKey) {
		t.Error("the API key reached the cassette file")
	}
	if strings.Contains(text, customerSecret) {
		t.Error("a registered secret reached the cassette file")
	}
	if strings.Contains(text, "Bearer") || strings.Contains(strings.ToLower(text), "authorization") {
		t.Error("an Authorization header reached the cassette file")
	}
	if !strings.Contains(text, cassette.Redacted) {
		t.Error("the secret was neither redacted nor present; scrubbing may not have run")
	}

	// The independent check must agree.
	if err := cassette.Verify(path, recordKey, customerSecret); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// TestVerifyCatchesALeak proves Verify is not vacuous.
func TestVerifyCatchesALeak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leaky.jsonl")
	line := `{"key":"abc","request":{"method":"POST","path":"/v1/systemone"},` +
		`"response":{"status":200,"body":{"note":"key is ` + recordKey + `"}}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := cassette.Verify(path, recordKey); err == nil {
		t.Error("Verify passed a file containing the key")
	}
}

// TestCassetteIsHumanDiffable: the format is a review surface, so a changed
// answer must show as a changed field rather than an opaque blob.
func TestCassetteIsHumanDiffable(t *testing.T) {
	raw, err := os.ReadFile(recordMixed(t, t.TempDir()))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 interaction per line", len(lines))
	}
	var in cassette.Interaction
	if err := json.Unmarshal([]byte(lines[0]), &in); err != nil {
		t.Fatalf("a cassette line must be valid JSON: %v", err)
	}
	if in.Request.Method != "POST" || in.Request.Path != "/v1/systemone" {
		t.Errorf("request = %+v", in.Request)
	}
	if in.Response.Status != 200 {
		t.Errorf("status = %d", in.Response.Status)
	}
	if !strings.Contains(string(in.Response.Body), "is_urgent") {
		t.Error("the response body should be readable in the file")
	}
	if strings.Contains(lines[0], typesafe.RequestIDHeader) {
		t.Error("a per-call request id was recorded; recordings would differ every time")
	}
}

// TestReplayPreservesErrors: a recorded failure must replay as the same typed
// error, or a test written against a cassette is not exercising the path it
// claims to.
func TestReplayPreservesErrors(t *testing.T) {
	srv := typesafetest.NewServer(t, typesafetest.Unprocessable(
		[]string{"body", "questions", "q", "choice", "criteria"}, "Field required", "missing"))

	path := filepath.Join(t.TempDir(), "err.jsonl")
	tr := cassette.NewRecord(path, cassette.WithTransport(redirectTo(srv.URL)))

	req := &typesafe.SystemOneRequest{
		State:     "x",
		Questions: map[string]typesafe.Question{"q": typesafe.Noul{Instructions: "?"}},
	}
	if _, err := clientFor(t, tr.Client()).SystemOne(context.Background(), req); err == nil {
		t.Fatal("expected the recorded error")
	}
	if err := tr.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err := clientFor(t, cassette.Replay(t, path)).SystemOne(context.Background(), req)
	var ue *typesafe.UnprocessableEntityError
	if !errors.As(err, &ue) {
		t.Fatalf("replayed error = %T (%v), want *UnprocessableEntityError", err, err)
	}
	if len(ue.Detail) != 1 {
		t.Fatalf("detail was not preserved: %+v", ue.Detail)
	}
	if got := ue.Detail[0].Path(); got != "body.questions.q.choice.criteria" {
		t.Errorf("Path() = %q", got)
	}
}

// TestUnplayedIsReported: a cassette holding interactions nothing asks for is
// usually a stale fixture.
func TestUnplayedIsReported(t *testing.T) {
	path := recordMixed(t, t.TempDir())
	tr, err := cassette.NewReplay(path)
	if err != nil {
		t.Fatalf("NewReplay: %v", err)
	}
	if got := tr.Cassette().Unplayed(); len(got) != 1 {
		t.Errorf("before replay: %d unplayed, want 1", len(got))
	}
	if _, err := clientFor(t, tr.Client()).SystemOne(context.Background(), mixedRequest()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := tr.Cassette().Unplayed(); len(got) != 0 {
		t.Errorf("after replay: %v still unplayed", got)
	}
}

func TestMissingCassetteSaysHowToRecordIt(t *testing.T) {
	fake := &recordingTB{}
	cassette.Replay(fake, filepath.Join(t.TempDir(), "absent.jsonl"))
	if !strings.Contains(fake.fatal, "TYPESAFE_UPDATE_CASSETTES") {
		t.Errorf("message should say how to record it, got: %q", fake.fatal)
	}
}

// recordingTB captures what a helper reports, so failure paths can be tested
// without failing the test doing the testing.
type recordingTB struct {
	fatal string
	errs  []string
}

func (r *recordingTB) Helper() {}
func (r *recordingTB) Fatalf(f string, a ...any) {
	if r.fatal == "" {
		r.fatal = sprintf(f, a...)
	}
}
func (r *recordingTB) Errorf(f string, a ...any) { r.errs = append(r.errs, sprintf(f, a...)) }
func (r *recordingTB) Cleanup(func())            {}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
