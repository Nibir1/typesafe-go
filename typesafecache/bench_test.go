package typesafecache_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/typesafecache"
)

// Benchmarks for PERFORMANCE.md.
//
// A cache hit replaces a 70-500ms network call, so the interesting number is
// not whether it is fast — it is four orders of magnitude faster whatever it
// does — but how much of it is the key derivation, which cannot be avoided and
// scales with the size of the request.

func benchClient(b *testing.B, c *typesafecache.Cache) *typesafe.Client {
	b.Helper()
	body, err := json.Marshal(map[string]any{
		"model":   "jev-1.13.0",
		"answers": map[string]any{"q": map[string]any{"type": "noul", "noul": 0.9}},
		"usage":   map[string]any{"input_tokens": 312, "output_tokens": 48},
	})
	if err != nil {
		b.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	b.Cleanup(srv.Close)

	client, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-bench"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		typesafe.WithInterceptor(c.Interceptor()),
	)
	if err != nil {
		b.Fatal(err)
	}
	return client
}

func benchRequest(state string) *typesafe.SystemOneRequest {
	return &typesafe.SystemOneRequest{
		State: state,
		Model: "jev-latest",
		Questions: typesafe.Questions{
			"q": typesafe.Noul{Instructions: "Does this convey urgency?"},
		},
	}
}

// BenchmarkCacheHit is a served hit through the interceptor: key derivation,
// lookup, and the response copy.
func BenchmarkCacheHit(b *testing.B) {
	for _, mode := range []struct {
		name string
		opts []typesafecache.Option
	}{
		{"copying", nil},
		{"shared", []typesafecache.Option{typesafecache.WithSharedResponses()}},
	} {
		b.Run(mode.name, func(b *testing.B) {
			c, err := typesafecache.New(mode.opts...)
			if err != nil {
				b.Fatal(err)
			}
			client := benchClient(b, c)
			req := benchRequest("a stable piece of state")
			ctx := context.Background()

			// Two calls to warm: the first learns where the alias resolves,
			// the second stores under the resolved key.
			for i := 0; i < 2; i++ {
				if _, err := client.SystemOne(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
			if s := c.Stats(); s.Hits == 0 {
				b.Fatal("the cache never hit; the benchmark would measure the API")
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := client.SystemOne(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkCacheMiss is the cost the cache adds when it does not hit: the key
// is still derived, and the response is still stored.
func BenchmarkCacheMiss(b *testing.B) {
	c, err := typesafecache.New(typesafecache.WithMaxEntries(16))
	if err != nil {
		b.Fatal(err)
	}
	client := benchClient(b, c)
	ctx := context.Background()

	// Warm the alias mapping so the miss path is a real miss rather than the
	// no-key-yet path.
	if _, err := client.SystemOne(ctx, benchRequest("warm")); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := client.SystemOne(ctx, benchRequest(fmt.Sprintf("state %d", i))); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCacheKeyDerivation isolates the unavoidable half: canonicalizing
// and hashing the request. It scales with the request, not with the cache.
func BenchmarkCacheKeyDerivation(b *testing.B) {
	// Capped at 40 KiB: a larger state trips the SDK's own single-question
	// token ceiling, which the first version of this benchmark discovered by
	// failing. A size the API would reject is not a size worth measuring.
	for _, size := range []int{1, 10, 40} {
		b.Run(fmt.Sprintf("%dkb-state", size), func(b *testing.B) {
			state := make([]byte, size*1024)
			for i := range state {
				state[i] = byte('a' + i%26)
			}

			c, err := typesafecache.New(typesafecache.WithSharedResponses())
			if err != nil {
				b.Fatal(err)
			}
			client := benchClient(b, c)
			req := benchRequest(string(state))
			ctx := context.Background()

			for i := 0; i < 2; i++ {
				if _, err := client.SystemOne(ctx, req); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := client.SystemOne(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
