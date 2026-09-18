// Package cassette records real API interactions to a file and replays them
// offline, so that an integration test needs a live key exactly once.
//
// It works at the http.RoundTripper layer, which means the whole client is
// exercised on replay: request marshalling, status mapping, the typed error
// hierarchy, response validation. A double that stubbed the SDK's own methods
// would skip all of that and test considerably less.
//
//	client, err := typesafe.NewClient(
//	    typesafe.WithAPIKey("replay"), // any non-empty value; never sent
//	    typesafe.WithHTTPClient(cassette.MustReplay("testdata/cassettes/triage.jsonl")),
//	)
//
// # Format
//
// Newline-delimited JSON, one interaction per line, committed to git. The
// format is deliberately diffable: when a cassette changes, the review shows
// which request and which field changed rather than an opaque blob.
//
// # Determinism
//
// Cassettes contain no timestamps, no durations, and no request ids. Recording
// the same interactions twice produces byte-identical files on any machine,
// which is what makes "re-record and check the diff is empty" a usable signal
// that the API has not drifted.
//
// # Secrets
//
// The Authorization header is never written. Any occurrence of the recorder's
// API key is replaced in both bodies and headers before anything touches disk.
// Scrubbing happens at write time, so a secret cannot reach the file even if
// the process crashes later.
package cassette

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/nibir1/typesafe-go/internal/canonical"
)

// Redacted replaces any secret found while recording.
const Redacted = "[REDACTED]"

// ErrNotFound means no recorded interaction matches the request.
var ErrNotFound = errors.New("cassette: no recorded interaction matches this request")

// Interaction is one recorded request/response pair.
type Interaction struct {
	// Key is the match key: a SHA-256 over the method, path, and canonical
	// request body. Requests hash to the same key regardless of JSON key
	// order, so a re-marshalled identical request still matches.
	Key string `json:"key"`

	Request  Request  `json:"request"`
	Response Response `json:"response"`
}

// Request is the recorded side of a call. Headers are omitted entirely: they
// carry the credential, they vary by Go version in ways that have nothing to
// do with the API, and nothing in matching depends on them.
type Request struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// Response is the recorded reply.
type Response struct {
	Status int `json:"status"`

	// Headers keeps only what changes client behavior — content-type and
	// retry-after. Per-call values such as x-typesafe-request-id are dropped,
	// because keeping them would make the file differ on every re-record.
	Headers map[string]string `json:"headers,omitempty"`

	Body json.RawMessage `json:"body,omitempty"`
}

// Cassette is a set of recorded interactions.
type Cassette struct {
	mu    sync.Mutex
	path  string
	byKey map[string]*Interaction
	order []string

	// plays counts replays per key, so a test can assert what was used.
	plays map[string]int
}

// New returns an empty cassette that will be written to path.
func New(path string) *Cassette {
	return &Cassette{
		path:  path,
		byKey: make(map[string]*Interaction),
		plays: make(map[string]int),
	}
}

// Load reads a cassette from disk.
func Load(path string) (*Cassette, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cassette: open %s: %w", path, err)
	}
	defer f.Close()

	c := New(path)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := bytes.TrimSpace(sc.Bytes())
		if len(text) == 0 {
			continue
		}
		var in Interaction
		if err := json.Unmarshal(text, &in); err != nil {
			return nil, fmt.Errorf("cassette: %s line %d: %w", path, line, err)
		}
		if in.Key == "" {
			return nil, fmt.Errorf("cassette: %s line %d: interaction has no key", path, line)
		}
		c.add(&in)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("cassette: read %s: %w", path, err)
	}
	return c, nil
}

func (c *Cassette) add(in *Interaction) {
	if _, seen := c.byKey[in.Key]; !seen {
		c.order = append(c.order, in.Key)
	}
	c.byKey[in.Key] = in
}

// Len is the number of distinct interactions held.
func (c *Cassette) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byKey)
}

// Plays reports how many times each key was replayed.
func (c *Cassette) Plays() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.plays))
	for k, v := range c.plays {
		out[k] = v
	}
	return out
}

// Unplayed returns the keys never matched during replay, sorted.
//
// A cassette accumulating interactions nothing asks for is usually a test that
// changed without its fixture being re-recorded.
func (c *Cassette) Unplayed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, k := range c.order {
		if c.plays[k] == 0 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Save writes the cassette to its path, creating parent directories.
//
// Interactions are written in recording order, and re-saving an unchanged
// cassette produces byte-identical output.
func (c *Cassette) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if dir := filepath.Dir(c.path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("cassette: mkdir %s: %w", dir, err)
		}
	}

	var buf bytes.Buffer
	for _, key := range c.order {
		in := c.byKey[key]
		line, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("cassette: encode interaction %s: %w", key, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(c.path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("cassette: write %s: %w", c.path, err)
	}
	return nil
}

// key derives the match key for a request.
func key(method, path string, body []byte) (string, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return canonical.HashParts(method, path)
	}
	return canonical.HashParts(method, path, body)
}

// keptHeaders are the response headers worth preserving: they change client
// behavior. Everything else is dropped, so that a cassette does not churn on
// server-side header changes unrelated to the API contract.
var keptHeaders = []string{"Content-Type", "Retry-After"}
