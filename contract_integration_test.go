//go:build integration

package typesafe_test

// Contract drift detection.
//
//	go test -tags=integration -run TestContractDrift ./...
//
// Requires TYPESAFE_API_KEY. This is the only test in the repository that
// touches the network, and it is excluded from the default build so that
// `go test ./...` stays offline and key-free (roadmap P5).
//
// It replays every golden request in testdata/contract against the live API
// and asserts that the response still has the shape the contract promises. It
// deliberately does NOT assert specific probabilities: the model's answers may
// drift, its response *shape* may not. A failure here means either the API
// changed or our reading of it was wrong — both are worth an issue.
//
// Phase 0 also uses this test to capture the parts the published docs do not
// specify: the 401 and 422 bodies, and whether retry-after appears on 429.
// Those runs write to testdata/contract/observed/ when -update is passed.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
)

var update = flag.Bool("update", false, "record observed error envelopes and headers")

const observedDir = "testdata/contract/observed"

func apiKey(t *testing.T) string {
	t.Helper()
	k := os.Getenv(typesafe.EnvAPIKey)
	if k == "" {
		t.Skipf("%s is not set", typesafe.EnvAPIKey)
	}
	return k
}

func baseURL() string {
	if u := os.Getenv(typesafe.EnvBaseURL); u != "" {
		return u
	}
	return typesafe.DefaultBaseURL
}

// post sends body to path and returns status, headers, and the raw response.
func post(t *testing.T, ctx context.Context, key, path string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL()+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "typesafe-go/"+typesafe.Version+" (contract-test)")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, buf.Bytes()
}

// TestContractDrift replays each golden request and checks the live response
// still matches the contract's shape.
func TestContractDrift(t *testing.T) {
	key := apiKey(t)
	ctx := context.Background()

	paths, err := filepath.Glob(filepath.Join(contractDir, "*.request.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}

	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			body, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read: %v", err)
			}

			status, hdr, raw := post(t, ctx, key, typesafe.SystemOnePath, body)
			if id := hdr.Get(typesafe.RequestIDHeader); id != "" {
				t.Logf("%s: %s", typesafe.RequestIDHeader, id)
			} else {
				t.Errorf("response carries no %s header", typesafe.RequestIDHeader)
			}
			if status != http.StatusOK {
				t.Fatalf("status %d, want 200; body: %s", status, raw)
			}

			var got map[string]any
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			if err := dec.Decode(&got); err != nil {
				t.Fatalf("decode: %v; body: %s", err, raw)
			}

			// Same expectations the offline suite enforces, against live data.
			want := readJSON(t, filepath.Join(filepath.Dir(p),
				filepath.Base(p[:len(p)-len(".request.json")])+".response.json"))

			assertSameShape(t, want, got, "")

			// The model that answered is reported, and may be a versioned id
			// rather than the alias we sent.
			if m, _ := got["model"].(string); m == "" {
				t.Error("response does not report the model that answered")
			} else {
				t.Logf("answered by model: %s", m)
			}
		})
	}
}

// assertSameShape compares structure and types, never values: probabilities
// are expected to drift, keys and types are not.
func assertSameShape(t *testing.T, want, got map[string]any, path string) {
	t.Helper()
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("%s%s: missing from the live response", path, k)
			continue
		}
		switch w := wv.(type) {
		case map[string]any:
			g, ok := gv.(map[string]any)
			if !ok {
				t.Errorf("%s%s: live type %T, fixture type object", path, k, gv)
				continue
			}
			assertSameShape(t, w, g, path+k+".")
		case []any:
			if _, ok := gv.([]any); !ok {
				t.Errorf("%s%s: live type %T, fixture type array", path, k, gv)
			}
		case json.Number:
			if _, ok := gv.(json.Number); !ok {
				t.Errorf("%s%s: live type %T, fixture type number", path, k, gv)
			}
		case string:
			if _, ok := gv.(string); !ok {
				t.Errorf("%s%s: live type %T, fixture type string", path, k, gv)
			}
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s%s: present live but absent from the fixture — the contract grew", path, k)
		}
	}
}

// TestObserveErrorEnvelopes captures what the published docs do not specify:
// the exact 401 and 422 bodies, and the headers that accompany them. This is
// Phase 0 task 4. Run with -update to write the observations.
func TestObserveErrorEnvelopes(t *testing.T) {
	key := apiKey(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		key    string
		body   string
		expect int
	}{
		{
			name:   "401_invalid_key",
			key:    "sk-definitely-not-a-valid-key",
			body:   `{"state":"hello","model":"jev-latest","questions":{"q":{"type":"noul","instructions":"Is this a greeting?"}}}`,
			expect: http.StatusUnauthorized,
		},
		{
			name:   "422_missing_state",
			key:    key,
			body:   `{"model":"jev-latest","questions":{"q":{"type":"noul","instructions":"x"}}}`,
			expect: http.StatusUnprocessableEntity,
		},
		{
			name:   "422_choice_without_criteria",
			key:    key,
			body:   `{"state":"hello","model":"jev-latest","questions":{"q":{"type":"choice","instructions":"x"}}}`,
			expect: http.StatusUnprocessableEntity,
		},
		{
			name:   "422_empty_questions",
			key:    key,
			body:   `{"state":"hello","model":"jev-latest","questions":{}}`,
			expect: http.StatusUnprocessableEntity,
		},
		{
			// The prose docs say a Score needs at least two levels; the
			// OpenAPI schema says minItems: 1. This resolves the discrepancy.
			name:   "score_single_level",
			key:    key,
			body:   `{"state":"hello","model":"jev-latest","questions":{"q":{"type":"score","instructions":"Rate it.","criteria":["only level"]}}}`,
			expect: 0, // whatever it is, record it
		},
		{
			name:   "422_unknown_model",
			key:    key,
			body:   `{"state":"hello","model":"jev-does-not-exist","questions":{"q":{"type":"noul","instructions":"x"}}}`,
			expect: 0,
		},
	}

	if *update {
		if err := os.MkdirAll(observedDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, hdr, raw := post(t, ctx, tc.key, typesafe.SystemOnePath, []byte(tc.body))
			t.Logf("status %d", status)
			t.Logf("body   %s", raw)
			for _, h := range []string{"retry-after", typesafe.RequestIDHeader, "content-type"} {
				if v := hdr.Get(h); v != "" {
					t.Logf("header %s: %s", h, v)
				}
			}
			if tc.expect != 0 && status != tc.expect {
				t.Errorf("status %d, want %d", status, tc.expect)
			}
			if status == http.StatusTooManyRequests && hdr.Get("retry-after") == "" {
				t.Error("429 without a retry-after header — Phase 4 must handle its absence")
			}
			if *update {
				rec := map[string]any{
					"request_body": json.RawMessage(tc.body),
					"status":       status,
					"headers":      redact(hdr),
					"body":         json.RawMessage(raw),
				}
				b, _ := json.MarshalIndent(rec, "", "  ")
				out := filepath.Join(observedDir, tc.name+".json")
				if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
					t.Fatalf("write %s: %v", out, err)
				}
				t.Logf("recorded %s", out)
			}
		})
	}
}

// redact drops per-call and sensitive headers so recordings stay deterministic
// and safe to commit.
func redact(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		switch {
		case eqFold(k, "authorization"), eqFold(k, typesafe.RequestIDHeader),
			eqFold(k, "date"), eqFold(k, "set-cookie"), eqFold(k, "cf-ray"):
			out[k] = "REDACTED"
		default:
			out[k] = v[0]
		}
	}
	return out
}

func eqFold(a, b string) bool { return http.CanonicalHeaderKey(a) == http.CanonicalHeaderKey(b) }

// TestModelsEndpoint confirms GET /v1/models returns the documented shape.
func TestModelsEndpoint(t *testing.T) {
	key := apiKey(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		baseURL()+typesafe.ModelsPath, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", typesafe.ModelsPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}

	var out struct {
		Models []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			ReleaseDate string `json:"release_date"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Models) == 0 {
		t.Fatal("models list is empty")
	}
	for i, m := range out.Models {
		if m.Name == "" || m.Description == "" || m.ReleaseDate == "" {
			t.Errorf("model %d has an empty required field: %+v", i, m)
		}
		t.Logf("model: %s (%s)", m.Name, m.ReleaseDate)
	}
}
