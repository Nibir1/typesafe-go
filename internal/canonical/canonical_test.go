package canonical

import (
	"encoding/json"
	"testing"
)

// TestKeyOrderDoesNotMatter is the property everything else rests on. Go
// randomizes map iteration, so without canonicalization the same request would
// hash differently on every run and no cassette would ever match twice.
func TestKeyOrderDoesNotMatter(t *testing.T) {
	a := map[string]any{"z": 1, "a": 2, "m": map[string]any{"y": 3, "b": 4}}
	want, err := Hash(a)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	for i := range 200 {
		got, err := Hash(map[string]any{"m": map[string]any{"b": 4, "y": 3}, "a": 2, "z": 1})
		if err != nil {
			t.Fatalf("Hash: %v", err)
		}
		if got != want {
			t.Fatalf("iteration %d: hash changed with key order", i)
		}
	}
}

func TestCanonicalOutput(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"sorts keys", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"sorts nested keys", `{"o":{"z":1,"a":2}}`, `{"o":{"a":2,"z":1}}`},
		{"strips whitespace", "{\n  \"a\" : 1\n}", `{"a":1}`},
		{"preserves array order", `[3,1,2]`, `[3,1,2]`},
		{"preserves nested array order", `{"lv":["c","a","b"]}`, `{"lv":["c","a","b"]}`},
		{"preserves null", `{"a":null}`, `{"a":null}`},
		{"preserves booleans", `{"a":true,"b":false}`, `{"a":true,"b":false}`},
		{"preserves numeric literals", `{"a":1,"b":1.0,"c":1e3}`, `{"a":1,"b":1.0,"c":1e3}`},
		{"escapes strings", `{"a":"x\"y"}`, `{"a":"x\"y"}`},
		{"handles unicode", `{"k":"café"}`, `{"k":"café"}`},
		{"empty object", `{}`, `{}`},
		{"empty array", `[]`, `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalizeJSON([]byte(tc.in))
			if err != nil {
				t.Fatalf("CanonicalizeJSON: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestArrayOrderIsSignificant: a Score rubric's order is its meaning, so
// reordering levels must change the hash. If it did not, two different rubrics
// would share a cassette entry.
func TestArrayOrderIsSignificant(t *testing.T) {
	a, err := Hash(map[string]any{"criteria": []any{"low", "high"}})
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	b, err := Hash(map[string]any{"criteria": []any{"high", "low"}})
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if a == b {
		t.Error("reordering a Score rubric must change the hash")
	}
}

// TestCanonicalIsValidJSON: the output is written into cassettes, so it has to
// survive a round trip.
func TestCanonicalIsValidJSON(t *testing.T) {
	in := `{"b":[1,{"z":null,"a":"x"}],"a":{"c":true}}`
	got, err := CanonicalizeJSON([]byte(in))
	if err != nil {
		t.Fatalf("CanonicalizeJSON: %v", err)
	}
	var v any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("canonical output is not valid JSON: %v (%s)", err, got)
	}

	// Canonicalizing twice must be a no-op.
	again, err := CanonicalizeJSON(got)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if string(again) != string(got) {
		t.Errorf("not idempotent:\nfirst:  %s\nsecond: %s", got, again)
	}
}

// TestHashPartsCannotCollideAcrossBoundaries: folding method and path in with
// the body must not let a regrouping collide. Without a separator,
// ("POST","/ab") and ("POST/a","b") would hash identically.
func TestHashPartsCannotCollideAcrossBoundaries(t *testing.T) {
	a, err := HashParts("POST", "/ab")
	if err != nil {
		t.Fatalf("HashParts: %v", err)
	}
	b, err := HashParts("POST/a", "b")
	if err != nil {
		t.Fatalf("HashParts: %v", err)
	}
	if a == b {
		t.Error("parts collided across their boundary; the separator is not doing its job")
	}

	// The same body on different endpoints must not share a key.
	body := []byte(`{"model":"jev-latest"}`)
	post, _ := HashParts("POST", "/v1/systemone", body)
	get, _ := HashParts("GET", "/v1/models", body)
	if post == get {
		t.Error("different endpoints produced the same key")
	}
}

func TestEmptyAndInvalidInput(t *testing.T) {
	for _, in := range []string{"", "   ", "\n"} {
		got, err := CanonicalizeJSON([]byte(in))
		if err != nil {
			t.Errorf("empty input %q: %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("empty input %q produced %q", in, got)
		}
	}
	if _, err := CanonicalizeJSON([]byte(`{"a":`)); err == nil {
		t.Error("malformed JSON should error")
	}
	if _, err := Canonicalize(make(chan int)); err == nil {
		t.Error("an unmarshalable value should error")
	}
}

// TestHashIsStable asserts the property that matters: equivalent input hashes
// identically, every time, regardless of key order.
//
// A literal expected digest is deliberately not pinned here. Pinning one would
// turn any change to canonicalization into a one-line test edit, when what it
// actually requires is a human deciding whether every committed cassette needs
// re-recording. Leaving it unpinned means that decision surfaces as failing
// cassette tests, which is where it belongs.
func TestHashIsStable(t *testing.T) {
	got, err := HashJSON([]byte(`{"model":"jev-latest","state":"hello"}`))
	if err != nil {
		t.Fatalf("HashJSON: %v", err)
	}
	if got == "" {
		t.Fatal("empty hash")
	}
	for range 100 {
		again, err := HashJSON([]byte(`{"state":"hello","model":"jev-latest"}`))
		if err != nil {
			t.Fatalf("HashJSON: %v", err)
		}
		if again != got {
			t.Fatal("hash is not stable for equivalent input")
		}
	}
}
