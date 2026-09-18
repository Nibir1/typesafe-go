package typesafecache

import (
	"time"

	typesafe "github.com/nibir1/typesafe-go"
)

// Test hooks for measuring the lookup itself, separately from deriving a key.
//
// The allocation criterion is about the hit path, and a key derivation that
// canonicalizes and hashes the request necessarily allocates. Exporting the
// two halves lets the test say which one it is measuring instead of measuring
// both and reporting the sum.

// ExportedPut stores a response under a fixed key and returns it.
func ExportedPut(c *Cache, resp *typesafe.SystemOneResponse) string {
	const key = "0000000000000000000000000000000000000000000000000000000000000000"
	c.put(key, resp)
	return key
}

// ExportedGet is the lookup, with the key already known.
func ExportedGet(c *Cache, key string) (*typesafe.SystemOneResponse, string, bool) {
	return c.get(key)
}

// ExportedSetClock replaces the cache's clock, so a test can age entries and
// alias mappings without sleeping.
func ExportedSetClock(c *Cache, now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}
