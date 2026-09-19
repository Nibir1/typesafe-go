// Package harness builds the cassette-backed client the example tests use.
//
// It exists only so that each example's main.go stays copy-pasteable: an
// example carrying test scaffolding teaches the scaffolding, not the API.
// Every main.go therefore calls typesafe.NewClient() exactly as yours would,
// and its run function takes a *typesafe.Client so a test can pass a replaying
// one instead.
package harness

import (
	"flag"
	"os"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/cassette"
)

// Update is the -update flag, shared by every example test.
//
//	go test ./... -update       # re-record every cassette against the live API
//	go test ./...               # replay, offline, no key needed
var Update = flag.Bool("update", false, "re-record cassettes against the live API")

// Client returns a client that replays path, or records it when -update is set.
//
// The model is pinned rather than left as jev-latest: an example's cassette
// should not silently start replaying answers from a different model version
// because an alias moved.
func Client(t *testing.T, path string) (*typesafe.Client, bool) {
	t.Helper()

	recording := cassette.Recording(*Update)
	hc := cassette.Open(t, path, recording, cassette.WithSecret(os.Getenv(typesafe.EnvAPIKey)))

	key := os.Getenv(typesafe.EnvAPIKey)
	if key == "" {
		key = "replay"
	}
	c, err := typesafe.NewClient(
		typesafe.WithAPIKey(key),
		typesafe.WithHTTPClient(hc),
		typesafe.WithDefaultModel("jev-latest"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, recording
}
