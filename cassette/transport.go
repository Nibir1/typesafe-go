package cassette

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Mode selects what a transport does with a request.
type Mode int

const (
	// ModeReplay serves from the cassette and never touches the network.
	// A request with no recorded match fails with ErrNotFound.
	ModeReplay Mode = iota

	// ModeRecord performs every request for real and records the result,
	// overwriting any existing cassette.
	ModeRecord
)

// Transport is an http.RoundTripper backed by a cassette.
type Transport struct {
	mode Mode
	cass *Cassette

	// next performs real requests in ModeRecord.
	next http.RoundTripper

	// secrets are redacted from anything written to disk.
	secrets []string
}

// Option configures a Transport.
type Option func(*Transport)

// WithTransport sets the underlying RoundTripper used when recording.
func WithTransport(rt http.RoundTripper) Option {
	return func(t *Transport) { t.next = rt }
}

// WithSecret registers a value to redact from recordings.
//
// The recorder already strips the Authorization header and the API key it
// observes. Use this for anything else that must not reach a committed file —
// an account id, a customer name inside a state.
func WithSecret(s string) Option {
	return func(t *Transport) {
		if s != "" {
			t.secrets = append(t.secrets, s)
		}
	}
}

// NewReplay returns a Transport that serves path offline.
func NewReplay(path string, opts ...Option) (*Transport, error) {
	c, err := Load(path)
	if err != nil {
		return nil, err
	}
	return newTransport(ModeReplay, c, opts...), nil
}

// NewRecord returns a Transport that performs requests for real and records
// them to path. Call Save when finished.
func NewRecord(path string, opts ...Option) *Transport {
	return newTransport(ModeRecord, New(path), opts...)
}

func newTransport(mode Mode, c *Cassette, opts ...Option) *Transport {
	t := &Transport{mode: mode, cass: c, next: http.DefaultTransport}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Cassette exposes the underlying cassette, for Plays, Unplayed, and Save.
func (t *Transport) Cassette() *Cassette { return t.cass }

// Save writes the cassette, and is a no-op in replay mode.
func (t *Transport) Save() error {
	if t.mode != ModeRecord {
		return nil
	}
	return t.cass.Save()
}

// Client wraps this Transport in an *http.Client ready for
// typesafe.WithHTTPClient.
func (t *Transport) Client() *http.Client { return &http.Client{Transport: t} }

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readBody(req)
	if err != nil {
		return nil, err
	}

	k, err := key(req.Method, req.URL.Path, body)
	if err != nil {
		return nil, err
	}

	if t.mode == ModeReplay {
		return t.replay(req, k, body)
	}
	return t.record(req, k, body)
}

func (t *Transport) replay(req *http.Request, k string, body []byte) (*http.Response, error) {
	t.cass.mu.Lock()
	in, ok := t.cass.byKey[k]
	if ok {
		t.cass.plays[k]++
	}
	t.cass.mu.Unlock()

	if !ok {
		// A miss is the most common failure here, and "not found" alone sends
		// people hunting. Say what was asked for and what is on the tape.
		return nil, fmt.Errorf("%w\n  request: %s %s\n  key:     %s\n  cassette: %s (%d interaction(s))\n"+
			"  re-record with -update, or check whether the request changed",
			ErrNotFound, req.Method, req.URL.Path, k, t.cass.path, t.cass.Len())
	}
	return buildResponse(req, in), nil
}

func (t *Transport) record(req *http.Request, k string, body []byte) (*http.Response, error) {
	// Capture the credential so it can be scrubbed, then let the real request
	// carry it as normal.
	secrets := t.secrets
	if auth := req.Header.Get("Authorization"); auth != "" {
		secrets = append(secrets, strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
	}

	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("cassette: read response body: %w", err)
	}

	in := &Interaction{
		Key: k,
		Request: Request{
			Method: req.Method,
			Path:   req.URL.Path,
			Body:   scrubJSON(body, secrets),
		},
		Response: Response{
			Status:  resp.StatusCode,
			Headers: keptResponseHeaders(resp.Header, secrets),
			Body:    scrubJSON(respBody, secrets),
		},
	}

	t.cass.mu.Lock()
	t.cass.add(in)
	t.cass.mu.Unlock()

	// Hand the caller a response reading from the bytes we already consumed.
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	return resp, nil
}

// readBody consumes and restores req.Body, since a RoundTripper must leave the
// request usable and the body is needed for the match key.
func readBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("cassette: read request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

func buildResponse(req *http.Request, in *Interaction) *http.Response {
	h := make(http.Header, len(in.Response.Headers))
	for k, v := range in.Response.Headers {
		h.Set(k, v)
	}
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	body := []byte(in.Response.Body)
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", in.Response.Status, http.StatusText(in.Response.Status)),
		StatusCode:    in.Response.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

func keptResponseHeaders(h http.Header, secrets []string) map[string]string {
	var out map[string]string
	for _, name := range keptHeaders {
		v := h.Get(name)
		if v == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(keptHeaders))
		}
		out[name] = scrubString(v, secrets)
	}
	return out
}

// scrubJSON redacts secrets from a JSON body, leaving it valid JSON.
func scrubJSON(b []byte, secrets []string) []byte {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return []byte(scrubString(string(b), secrets))
}

func scrubString(s string, secrets []string) string {
	for _, sec := range secrets {
		// Guard against a one-character or empty "secret" turning the whole
		// recording into redaction markers.
		if len(sec) < 8 {
			continue
		}
		s = strings.ReplaceAll(s, sec, Redacted)
	}
	return s
}

// Recording reports whether the -update flag or the TYPESAFE_UPDATE_CASSETTES
// environment variable asked for cassettes to be re-recorded.
//
// The env var exists so that CI can refresh every cassette in one run without
// each test package having to define its own flag.
func Recording(updateFlag bool) bool {
	if updateFlag {
		return true
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TYPESAFE_UPDATE_CASSETTES")))
	return v == "1" || v == "true" || v == "yes"
}
