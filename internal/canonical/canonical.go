// Package canonical produces a deterministic byte form of a JSON value.
//
// Two things in this SDK need to decide whether two requests are "the same":
// the cassette matcher, which looks up a recorded response, and the response
// cache in typesafecache. Both hash the request, and a hash is only useful if
// semantically identical requests produce identical bytes.
//
// The problem is Go map iteration order, which is deliberately randomized.
// Marshalling map[string]any twice can yield different key orders, so hashing
// json.Marshal output directly gives a different key for the same request on
// every run. encoding/json does sort map keys, but only for map types — a
// struct marshals in field-declaration order, and json.RawMessage passes
// through byte for byte.
//
// Canonicalize sidesteps all of that by routing every value through the
// generic JSON shape and re-emitting it with sorted keys.
//
// # What is and is not normalized
//
// Object keys are sorted; insignificant whitespace is removed; arrays keep
// their order, since order is meaning in a Score rubric.
//
// Numbers are preserved as written. 1 and 1.0 are semantically equal JSON but
// canonicalize differently, because they are different bytes on the wire and
// this package's job is byte determinism, not semantic equivalence. In
// practice a request marshalled by this SDK is consistent with itself, which
// is what the matcher needs.
package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// Canonicalize returns v as JSON with every object's keys sorted and no
// insignificant whitespace.
func Canonicalize(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonical: marshal: %w", err)
	}
	return CanonicalizeJSON(raw)
}

// CanonicalizeJSON canonicalizes JSON that is already encoded.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numeric literals exactly as written
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("canonical: decode: %w", err)
	}

	var buf bytes.Buffer
	if err := write(&buf, tree); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Hash returns the SHA-256 of the canonical form, hex encoded.
//
// This is the cassette match key and the cache key. It is stable across runs,
// machines, and Go versions: nothing in it depends on map iteration order,
// pointer values, or the platform.
func Hash(v any) (string, error) {
	b, err := Canonicalize(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// HashJSON is Hash for already-encoded JSON.
func HashJSON(raw []byte) (string, error) {
	b, err := CanonicalizeJSON(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// HashParts hashes several values as one unit, with a separator that cannot
// occur in JSON so that no regrouping of the parts collides.
//
// Used to fold the HTTP method and path in with the request body, so that a
// POST to /v1/systemone never matches a GET to /v1/models that happens to
// carry the same bytes.
func HashParts(parts ...any) (string, error) {
	h := sha256.New()
	for _, p := range parts {
		var b []byte
		switch v := p.(type) {
		case string:
			b = []byte(v)
		case []byte:
			var err error
			if b, err = CanonicalizeJSON(v); err != nil {
				return "", err
			}
		default:
			var err error
			if b, err = Canonicalize(v); err != nil {
				return "", err
			}
		}
		h.Write(b)
		h.Write([]byte{0x1e}) // ASCII record separator: never legal in JSON text
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func write(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")

	case bool:
		buf.WriteString(strconv.FormatBool(t))

	case json.Number:
		buf.WriteString(t.String())

	case string:
		return writeString(buf, t)

	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := write(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')

	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := write(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')

	default:
		// Decoding with UseNumber yields only the cases above. Anything else
		// means the input did not come through the decoder, so fall back to
		// encoding/json rather than silently dropping it.
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("canonical: unexpected value %T: %w", t, err)
		}
		buf.Write(b)
	}
	return nil
}

// writeString emits a JSON string using encoding/json's escaping, so that the
// canonical form stays valid JSON and matches what a normal marshal produces.
func writeString(buf *bytes.Buffer, s string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("canonical: encode string: %w", err)
	}
	buf.Write(b)
	return nil
}
