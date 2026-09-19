package genexample_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/cassette"
	"github.com/nibir1/typesafe-go/internal/genexample"
)

var update = flag.Bool("update", false, "re-record the cassette against the live API")

const cassettePath = "testdata/cassettes/ticket.jsonl"

// repoRoot is this file's ../.. — the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this source file")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return root
}

// The generated file is checked in, as go:generate output should be. This
// fails when it drifts from the declarations it was generated from — a
// generator whose output is not asserted to be current is a generator that
// quietly rots, and the first symptom is a question that no longer matches the
// enum the code switches on.
func TestGeneratedOutputIsCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the generator")
	}
	root := repoRoot(t)
	pkgDir := filepath.Join(root, "internal", "genexample")

	// Generate into a temp file rather than over the checked-in one: a
	// failing test must not leave the tree already changed to match itself.
	fresh := filepath.Join(t.TempDir(), "out.go")
	out, err := exec.Command("go", "run",
		filepath.Join(root, "cmd", "typesafe-gen"),
		"-type", "TicketQuestions",
		"-dir", pkgDir,
		"-o", fresh,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("typesafe-gen: %v\n%s", err, out)
	}

	want := normalizeEOL(mustRead(t, fresh))
	committed := filepath.Join(pkgDir, "ticketquestions_typesafe.go")
	got := normalizeEOL(mustRead(t, committed))

	if string(got) != string(want) {
		t.Errorf("%s is out of date. Regenerate it:\n"+
			"  go generate ./internal/genexample\n\n"+
			"--- checked in ---\n%s\n--- freshly generated ---\n%s",
			committed, got, want)
	}
}

// normalizeEOL makes the comparison independent of how git checked the file
// out.
//
// The generator emits LF. Git on Windows checks out CRLF unless told otherwise,
// so a byte comparison reported this file as out of date on Windows and only on
// Windows — the rest of the matrix was green, which is what made it confusing.
//
// .gitattributes now pins LF everywhere and is the real fix. This stays because
// .gitattributes does not renormalize a working tree that already exists: a
// contributor who cloned before it was added still has CRLF on disk, and a test
// that fails for them teaches nothing about the generator.
func normalizeEOL(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// The generated questions must produce a request the real API accepts, and an
// answer the generated accessors decode. The cassette makes that assertion
// offline after one live recording:
//
//	go test ./internal/genexample -update
func TestGeneratedQuestionsRoundTrip(t *testing.T) {
	hc := cassette.Open(t, cassettePath, cassette.Recording(*update),
		cassette.WithSecret(os.Getenv(typesafe.EnvAPIKey)))

	key := os.Getenv(typesafe.EnvAPIKey)
	if key == "" {
		key = "replay"
	}
	client, err := typesafe.NewClient(
		typesafe.WithAPIKey(key),
		typesafe.WithHTTPClient(hc),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var q genexample.TicketQuestions
	req := &typesafe.SystemOneRequest{
		State: map[string]any{
			"subject": "Card declined on renewal, and the dashboard is 500ing",
			"body": "My subscription renewal was declined twice today and I was " +
				"charged anyway. The billing page also returns a 500 error.",
		},
		Model:     "jev-latest",
		Questions: q.Questions(),
	}

	// The generated questions must pass the SDK's own validation before they
	// reach the wire.
	warnings, err := req.Validate()
	if err != nil {
		t.Fatalf("generated questions failed validation: %v", err)
	}
	for _, w := range warnings {
		t.Logf("warning: %s", w)
	}

	resp, err := client.SystemOne(context.Background(), req)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	dept, err := q.DepartmentAnswer(resp)
	if err != nil {
		t.Fatalf("DepartmentAnswer: %v", err)
	}
	// The accessor checks the answer against the declared option set, so
	// reaching here means no drift. The switch is typed: a typo in an arm
	// would not compile.
	switch dept.Choice {
	case genexample.TopicBilling, genexample.TopicTechnical,
		genexample.TopicSales, genexample.TopicOther:
	default:
		t.Fatalf("department %q is outside the declared set", dept.Choice)
	}
	if dept.Confidence < 0 || dept.Confidence > 1 {
		t.Errorf("confidence %v is outside [0,1]", dept.Confidence)
	}
	if len(dept.Probabilities) != len(genexample.AllTopic) {
		t.Errorf("%d probabilities, want one per declared option (%d)",
			len(dept.Probabilities), len(genexample.AllTopic))
	}

	sev, err := q.SeverityAnswer(resp)
	if err != nil {
		t.Fatalf("SeverityAnswer: %v", err)
	}
	if len(sev.Legend) != len(genexample.AllSeverity) {
		t.Errorf("legend has %d levels, want %d", len(sev.Legend), len(genexample.AllSeverity))
	}
	// Every declared level must come back, keyed by the typed value.
	for _, lvl := range genexample.AllSeverity {
		if _, ok := sev.Legend[lvl]; !ok {
			t.Errorf("level %d missing from the legend", lvl)
		}
	}
	// Thresholding on the typed level is the point of the whole exercise.
	if p := sev.AtOrAbove(genexample.SevNone); p < 0.99 {
		t.Errorf("AtOrAbove(SevNone) = %v, want the whole distribution", p)
	}
	if p := sev.AtOrBelow(genexample.SevCritical); p < 0.99 {
		t.Errorf("AtOrBelow(SevCritical) = %v, want the whole distribution", p)
	}

	t.Logf("department=%s (%.2f confidence), severity=%.2f (nearest %d)",
		dept.Choice, dept.Confidence, sev.Score, sev.Level)

	// Only meaningful on replay: a recording run writes interactions, it does
	// not play them.
	if !cassette.Recording(*update) {
		cassette.AssertAllPlayed(t, hc)
	}
}

// The cassette must not carry the key that recorded it.
func TestCassetteHasNoSecrets(t *testing.T) {
	if _, err := os.Stat(cassettePath); err != nil {
		t.Skipf("no cassette recorded yet: %v", err)
	}
	secrets := []string{}
	if k := os.Getenv(typesafe.EnvAPIKey); k != "" {
		secrets = append(secrets, k)
	}
	if err := cassette.Verify(cassettePath, secrets...); err != nil {
		t.Fatalf("cassette: %v", err)
	}
}
