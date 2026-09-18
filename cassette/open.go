package cassette

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
)

// TB is the subset of *testing.T this package needs.
//
// Declared structurally rather than importing "testing", so that nothing here
// drags the testing package into a production binary. *testing.T and
// *testing.B satisfy it.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
	Cleanup(func())
}

// MustReplay loads path and returns an *http.Client that serves from it,
// panicking if the cassette cannot be read.
//
// For use in an init or a package-level variable. In a test, prefer Replay,
// which reports the failure through the test rather than taking the process
// down with it.
func MustReplay(path string, opts ...Option) *http.Client {
	t, err := NewReplay(path, opts...)
	if err != nil {
		panic(err)
	}
	return t.Client()
}

// Replay returns an *http.Client serving path offline, failing the test if the
// cassette is missing or malformed.
//
// A missing cassette is reported with the command that would create it, since
// that is invariably the next thing the reader wants to know.
func Replay(tb TB, path string, opts ...Option) *http.Client {
	tb.Helper()
	tr, err := NewReplay(path, opts...)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			tb.Fatalf("cassette %s does not exist.\n"+
				"Record it once against the live API:\n"+
				"  TYPESAFE_UPDATE_CASSETTES=1 go test -tags=integration ./...", path)
		}
		tb.Fatalf("%v", err)
	}
	return tr.Client()
}

// Open returns an *http.Client that records when recording is true and replays
// otherwise, saving the cassette automatically when the test finishes.
//
// This is the usual entry point. One test body serves both modes: run it
// normally and it is offline and fast; run it with -update once and the
// fixture is refreshed from the live API.
//
//	var update = flag.Bool("update", false, "re-record cassettes")
//
//	func TestTriage(t *testing.T) {
//	    hc := cassette.Open(t, "testdata/cassettes/triage.jsonl",
//	        cassette.Recording(*update))
//	    client, err := typesafe.NewClient(
//	        typesafe.WithAPIKey(apiKeyOr(t, "replay")),
//	        typesafe.WithHTTPClient(hc),
//	    )
//	    // ...
//	}
//
// In recording mode the cassette is saved during cleanup, so a test that fails
// mid-way still leaves behind whatever it managed to capture.
func Open(tb TB, path string, recording bool, opts ...Option) *http.Client {
	tb.Helper()

	if !recording {
		if _, err := os.Stat(path); err == nil {
			return Replay(tb, path, opts...)
		} else if !errors.Is(err, fs.ErrNotExist) {
			tb.Fatalf("cassette: stat %s: %v", path, err)
		}
		tb.Fatalf("cassette %s does not exist and recording is off.\n"+
			"Record it once against the live API:\n"+
			"  TYPESAFE_UPDATE_CASSETTES=1 go test -tags=integration ./...", path)
		return nil
	}

	tr := NewRecord(path, opts...)
	tb.Cleanup(func() {
		if err := tr.Save(); err != nil {
			tb.Errorf("cassette: save %s: %v", path, err)
		}
	})
	return tr.Client()
}

// AssertAllPlayed fails the test if the cassette holds interactions that were
// never matched.
//
// Worth calling at the end of a test that is meant to exercise a whole
// cassette: an unplayed interaction usually means the request changed and the
// fixture was not re-recorded, which otherwise shows up much later as a
// confusing miss.
func AssertAllPlayed(tb TB, hc *http.Client) {
	tb.Helper()
	tr, ok := hc.Transport.(*Transport)
	if !ok {
		tb.Fatalf("cassette: client is not backed by a cassette transport")
		return
	}
	if unplayed := tr.Cassette().Unplayed(); len(unplayed) > 0 {
		tb.Errorf("cassette %s has %d unplayed interaction(s): %v\n"+
			"the requests under test may have changed since it was recorded",
			tr.Cassette().path, len(unplayed), unplayed)
	}
}

// Verify checks that a saved cassette carries no credential.
//
// Recording scrubs at write time, so this should never fire. It exists because
// "should never" is not an assertion, and a leaked key in a committed fixture
// is the one mistake in this package that cannot be undone by a later commit.
func Verify(path string, secrets ...string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cassette: verify %s: %w", path, err)
	}
	text := string(b)
	for _, s := range secrets {
		if len(s) < 8 {
			continue
		}
		if strings.Contains(text, s) {
			return fmt.Errorf("cassette: %s contains a secret", path)
		}
	}
	for _, marker := range []string{"Bearer ", "authorization", "Authorization"} {
		if strings.Contains(text, marker) {
			return fmt.Errorf("cassette: %s contains %q, which suggests a credential was recorded", path, marker)
		}
	}
	return nil
}
