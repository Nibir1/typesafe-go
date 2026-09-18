package typesafecache_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/typesafecache"
)

// --- helpers -----------------------------------------------------------------

// server answers every request as model, counting calls.
func server(t *testing.T, model *string, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   *model,
			"answers": map[string]any{"is_urgent": map[string]any{"type": "noul", "noul": 0.92}},
			"usage":   map[string]any{"input_tokens": 312, "output_tokens": 48},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func clientFor(t *testing.T, srv *httptest.Server, c *typesafecache.Cache) *typesafe.Client {
	t.Helper()
	client, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test-key"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.NoRetry()),
		typesafe.WithInterceptor(c.Interceptor()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func request(state string) *typesafe.SystemOneRequest {
	return &typesafe.SystemOneRequest{
		State: state,
		Model: "jev-latest",
		Questions: map[string]typesafe.Question{
			"is_urgent": typesafe.Noul{Instructions: "Does this convey urgency?"},
		},
	}
}

// --- the basics --------------------------------------------------------------

// The first call always reaches the API: nothing yet knows which model the
// alias resolves to, so no key can be derived.
func TestSecondIdenticalRequestIsServedFromCache(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, err := typesafecache.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := clientFor(t, srv, c)

	for i := 0; i < 5; i++ {
		resp, err := client.SystemOne(context.Background(), request("same state"))
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if resp.Model != "jev-1.13.0" {
			t.Errorf("call %d returned model %q", i, resp.Model)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("server saw %d calls, want 1 (the rest should be cached)", got)
	}
	if s := c.Stats(); s.Hits != 4 || s.Misses != 1 {
		t.Errorf("hits=%d misses=%d, want 4 and 1", s.Hits, s.Misses)
	}
	if s := c.Stats(); s.HitRate() != 0.8 {
		t.Errorf("HitRate = %v, want 0.8", s.HitRate())
	}
}

func TestDifferentStateIsADifferentKey(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, _ := typesafecache.New()
	client := clientFor(t, srv, c)

	for i := 0; i < 4; i++ {
		if _, err := client.SystemOne(context.Background(), request(fmt.Sprintf("state %d", i))); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("server saw %d calls, want 4 — distinct states must not share a key", got)
	}
}

// The key covers the questions too, not just the state.
func TestDifferentQuestionsAreADifferentKey(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, _ := typesafecache.New()
	client := clientFor(t, srv, c)

	base := request("one state")
	if _, err := client.SystemOne(context.Background(), base); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := client.SystemOne(context.Background(), base); err != nil {
		t.Fatalf("second: %v", err)
	}

	changed := request("one state")
	changed.Questions = map[string]typesafe.Question{
		"is_urgent": typesafe.Noul{Instructions: "Is this urgent at all?"}, // reworded
	}
	if _, err := client.SystemOne(context.Background(), changed); err != nil {
		t.Fatalf("third: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Errorf("server saw %d calls, want 2: identical then reworded", got)
	}
}

// --- the correctness property this cache exists to get right -----------------

// The exit criterion: the key must change when the resolved model changes,
// even though the alias the caller asked for did not.
//
// This is the whole reason the cache keys on the resolved id. Keyed on the
// alias, the assertion below would see one call and a stale answer from the
// previous model version, with nothing in the response to reveal it.
func TestAliasMoveInvalidatesCleanly(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, err := typesafecache.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := clientFor(t, srv, c)

	// Warm the cache under 1.13.0.
	for i := 0; i < 3; i++ {
		if _, err := client.SystemOne(context.Background(), request("stable state")); err != nil {
			t.Fatalf("warm %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("server saw %d calls while warming, want 1", got)
	}

	// The alias moves. The caller's request is byte-identical.
	model = "jev-1.14.0"

	// The cached alias mapping is still live, so this hit is served from the
	// old model — the documented TTL window.
	resp, err := client.SystemOne(context.Background(), request("stable state"))
	if err != nil {
		t.Fatalf("during the TTL window: %v", err)
	}
	if resp.Model != "jev-1.13.0" {
		t.Errorf("model = %q; within the TTL the old answer is served, by design", resp.Model)
	}

	// Once the mapping expires, one live request re-checks where the alias
	// points, and the move is discovered. Age the cache rather than purging
	// it: expiry is what happens in production, and it keeps the recorded
	// mapping around to compare against.
	base := time.Now()
	typesafecache.ExportedSetClock(c, func() time.Time { return base.Add(time.Hour) })

	resp, err = client.SystemOne(context.Background(), request("stable state"))
	if err != nil {
		t.Fatalf("after the mapping expired: %v", err)
	}
	if resp.Model != "jev-1.14.0" {
		t.Fatalf("model = %q, want jev-1.14.0 after the move", resp.Model)
	}

	// And the new answer is cached under the new id, not mixed with the old.
	before := calls.Load()
	resp, err = client.SystemOne(context.Background(), request("stable state"))
	if err != nil {
		t.Fatalf("after the move: %v", err)
	}
	if resp.Model != "jev-1.14.0" {
		t.Errorf("model = %q, want the new model from cache", resp.Model)
	}
	if calls.Load() != before {
		t.Error("the post-move response was not cached")
	}
}

// The move must be *reported*, because nothing else tells a caller that the
// model behind their alias changed.
func TestAliasMoveIsReported(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	var moved atomic.Bool
	c, _ := typesafecache.New(typesafecache.WithObserver(func(e typesafecache.Event) {
		if e.AliasMoved {
			moved.Store(true)
		}
		if e.Alias != "jev-latest" {
			t.Errorf("unexpected alias %q in event", e.Alias)
		}
	}))
	client := clientFor(t, srv, c)

	if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if moved.Load() {
		t.Error("the first response cannot be a move; there was nothing to move from")
	}

	model = "jev-1.14.0"
	base := time.Now()
	typesafecache.ExportedSetClock(c, func() time.Time { return base.Add(time.Hour) })
	if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("second: %v", err)
	}
	if !moved.Load() {
		t.Error("an alias move was not reported")
	}
}

// A pinned model id has no alias to move, so nothing about this machinery can
// bite: the first request already knows its own key.
func TestPinnedModelStillCaches(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, _ := typesafecache.New()
	client := clientFor(t, srv, c)

	req := request("s")
	req.Model = "jev-1.13.0"

	for i := 0; i < 3; i++ {
		if _, err := client.SystemOne(context.Background(), req); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("server saw %d calls, want 1", got)
	}
}

// --- failures are never cached -----------------------------------------------

// A cached error is a cached outage.
func TestFailuresAreNotCached(t *testing.T) {
	var calls atomic.Int64
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"detail": "boom"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "jev-1.13.0",
			"answers": map[string]any{"is_urgent": map[string]any{"type": "noul", "noul": 0.9}},
			"usage":   map[string]any{"input_tokens": 10, "output_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)

	c, _ := typesafecache.New()
	client := clientFor(t, srv, c)

	for i := 0; i < 3; i++ {
		if _, err := client.SystemOne(context.Background(), request("s")); err == nil {
			t.Fatalf("call %d should have failed", i)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("server saw %d calls, want 3 — a failure must not be cached", got)
	}

	// And recovery works: the cache did not poison the key.
	fail = false
	if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

// --- expiry and bounds -------------------------------------------------------

func TestEntriesExpire(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, err := typesafecache.New(typesafecache.WithTTL(40 * time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := clientFor(t, srv, c)

	if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("server saw %d calls before expiry, want 1", got)
	}

	time.Sleep(60 * time.Millisecond)

	if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("server saw %d calls after expiry, want 2", got)
	}
}

func TestMaxEntriesEvictsLeastRecentlyUsed(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, err := typesafecache.New(typesafecache.WithMaxEntries(2))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := clientFor(t, srv, c)

	ctx := context.Background()
	// Prime the alias mapping, then fill past the bound.
	for _, s := range []string{"a", "b", "c"} {
		if _, err := client.SystemOne(ctx, request(s)); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if s := c.Stats(); s.Entries > 2 {
		t.Errorf("cache holds %d entries, bound was 2", s.Entries)
	}
	if s := c.Stats(); s.Evictions == 0 {
		t.Error("nothing was evicted despite exceeding the bound")
	}

	// "a" is the oldest and should be gone; "c" should still be there.
	before := calls.Load()
	if _, err := client.SystemOne(ctx, request("c")); err != nil {
		t.Fatalf("c again: %v", err)
	}
	if calls.Load() != before {
		t.Error("the most recent entry was evicted")
	}
}

// --- the disk tier -----------------------------------------------------------

func TestDiskTierSurvivesANewCache(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)
	dir := t.TempDir()

	// First process: warm the cache.
	c1, err := typesafecache.New(typesafecache.WithDisk(dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client1 := clientFor(t, srv, c1)
	if _, err := client1.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if _, err := client1.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("warm hit: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("server saw %d calls while warming, want 1", got)
	}

	// Second "process": a fresh cache over the same directory. Its alias map
	// is empty, so the first request goes live to learn where jev-latest
	// points — and the second is served from disk.
	c2, err := typesafecache.New(typesafecache.WithDisk(dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client2 := clientFor(t, srv, c2)

	if _, err := client2.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("relearn: %v", err)
	}
	before := calls.Load()
	if _, err := client2.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("disk hit: %v", err)
	}
	if calls.Load() != before {
		t.Error("the entry was not served from disk")
	}

	// The tier is a flat directory of one file per key.
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(files) == 0 {
		t.Error("nothing was written to disk")
	}
}

func TestDiskCorruptionIsSurvivable(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)
	dir := t.TempDir()

	var errs atomic.Int64
	c, err := typesafecache.New(
		typesafecache.WithDisk(dir),
		typesafecache.WithObserver(func(e typesafecache.Event) {
			if e.Err != nil {
				errs.Add(1)
			}
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := clientFor(t, srv, c)

	if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("warm: %v", err)
	}

	// Corrupt every file on disk, and drop the memory tier so a lookup has to
	// read one.
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.Name()), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("corrupt: %v", err)
		}
	}

	c2, err := typesafecache.New(typesafecache.WithDisk(dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client2 := clientFor(t, srv, c2)

	// Two calls: the first relearns the alias, the second would read the
	// corrupt file. Neither may fail the request.
	for i := 0; i < 2; i++ {
		if _, err := client2.SystemOne(context.Background(), request("s")); err != nil {
			t.Fatalf("a corrupt cache entry must not fail the request: %v", err)
		}
	}

	// And the corrupt file is gone, rather than failing every lookup forever.
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, f.Name())); err == nil {
			b, _ := os.ReadFile(filepath.Join(dir, f.Name()))
			if string(b) == "{not json" {
				t.Errorf("%s is still corrupt on disk", f.Name())
			}
		}
	}
}

func TestPurgeDisk(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)
	dir := t.TempDir()

	c, _ := typesafecache.New(typesafecache.WithDisk(dir))
	client := clientFor(t, srv, c)
	if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if err := c.PurgeDisk(); err != nil {
		t.Fatalf("PurgeDisk: %v", err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Errorf("%d files survived PurgeDisk", len(files))
	}
}

// --- allocations -------------------------------------------------------------

// The exit criterion, stated precisely.
//
// A hit allocates nothing *once the key is known*. Deriving the key does
// allocate — it canonicalizes the request to JSON and hashes it, which cannot
// be done without touching the heap — so an end-to-end "zero-allocation hit"
// is not achievable and this measures the part that is.
func TestHitPathDoesNotAllocate(t *testing.T) {
	c, err := typesafecache.New(typesafecache.WithSharedResponses())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp := &typesafe.SystemOneResponse{
		Model:   "jev-1.13.0",
		Answers: map[string]json.RawMessage{"a": json.RawMessage(`{"type":"noul","noul":0.9}`)},
	}
	key := typesafecache.ExportedPut(c, resp)

	// Warm, so the first lookup's lazily-built internals are not counted.
	if _, _, ok := typesafecache.ExportedGet(c, key); !ok {
		t.Fatal("the entry was not stored")
	}

	allocs := testing.AllocsPerRun(1000, func() {
		if _, _, ok := typesafecache.ExportedGet(c, key); !ok {
			t.Fatal("miss during the allocation measurement")
		}
	})
	if allocs != 0 {
		t.Errorf("a shared-response hit allocated %v times, want 0", allocs)
	}
}

// The default copies, which costs exactly one map allocation per hit. Asserted
// so the trade stays visible rather than drifting.
func TestCopyingHitAllocatesOnce(t *testing.T) {
	c, err := typesafecache.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp := &typesafe.SystemOneResponse{
		Model:   "jev-1.13.0",
		Answers: map[string]json.RawMessage{"a": json.RawMessage(`{"type":"noul","noul":0.9}`)},
	}
	key := typesafecache.ExportedPut(c, resp)
	typesafecache.ExportedGet(c, key)

	allocs := testing.AllocsPerRun(1000, func() {
		typesafecache.ExportedGet(c, key)
	})
	// Measured at 3: the response struct, the answer map, and its bucket
	// array. The bound is loose because map internals are not a stable
	// promise across Go versions; what matters is that copying costs a small
	// constant and that it is visibly not free.
	if allocs > 4 {
		t.Errorf("a copying hit allocated %v times, want a small constant", allocs)
	}
	if allocs == 0 {
		t.Error("a copying hit allocated nothing; it is no longer copying")
	}
}

// A copied response must be independent: mutating what a caller was handed
// must not change what the next lookup returns.
func TestCopiedResponsesAreIndependent(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, _ := typesafecache.New()
	client := clientFor(t, srv, c)

	ctx := context.Background()
	if _, err := client.SystemOne(ctx, request("s")); err != nil {
		t.Fatalf("warm: %v", err)
	}
	first, err := client.SystemOne(ctx, request("s"))
	if err != nil {
		t.Fatalf("first hit: %v", err)
	}

	first.Answers["injected"] = json.RawMessage(`{"type":"noul","noul":0}`)
	first.Model = "tampered"

	second, err := client.SystemOne(ctx, request("s"))
	if err != nil {
		t.Fatalf("second hit: %v", err)
	}
	if _, bad := second.Answers["injected"]; bad {
		t.Error("mutating a returned response changed the cached entry")
	}
	if second.Model != "jev-1.13.0" {
		t.Errorf("cached model became %q", second.Model)
	}
}

// --- concurrency -------------------------------------------------------------

func TestConcurrentUse(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, _ := typesafecache.New(typesafecache.WithMaxEntries(32))
	client := clientFor(t, srv, c)

	// Prime the alias mapping so the workers exercise hits, not just misses.
	if _, err := client.SystemOne(context.Background(), request("warm")); err != nil {
		t.Fatalf("warm: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := client.SystemOne(context.Background(), request(fmt.Sprintf("s%d", i%8))); err != nil {
					t.Errorf("worker %d: %v", i, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	s := c.Stats()
	if s.Hits == 0 {
		t.Error("no hits under concurrent load")
	}
	if s.Entries > 32 {
		t.Errorf("cache holds %d entries, bound was 32", s.Entries)
	}
}

// --- degradation -------------------------------------------------------------

// A closed cache must behave like no cache, not like an error.
func TestClosedCacheDegradesToNoCache(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, _ := typesafecache.New()
	client := clientFor(t, srv, c)

	if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	before := calls.Load()
	for i := 0; i < 3; i++ {
		if _, err := client.SystemOne(context.Background(), request("s")); err != nil {
			t.Fatalf("after close: %v", err)
		}
	}
	if calls.Load() != before+3 {
		t.Error("a closed cache still served entries")
	}
}

func TestNilRequestPassesThrough(t *testing.T) {
	model := "jev-1.13.0"
	var calls atomic.Int64
	srv := server(t, &model, &calls)

	c, _ := typesafecache.New()
	client := clientFor(t, srv, c)

	// The client rejects a nil request; the cache must not panic on the way.
	if _, err := client.SystemOne(context.Background(), nil); err == nil {
		t.Error("a nil request should fail")
	}
}

// --- options -----------------------------------------------------------------

func TestOptionValidation(t *testing.T) {
	if _, err := typesafecache.New(typesafecache.WithTTL(-time.Second)); err == nil {
		t.Error("a negative TTL should be rejected")
	}
	if _, err := typesafecache.New(typesafecache.WithMaxEntries(-1)); err == nil {
		t.Error("a negative bound should be rejected")
	}
	if _, err := typesafecache.New(typesafecache.WithDisk("")); err == nil {
		t.Error("an empty disk directory should be rejected")
	}
	if _, err := typesafecache.New(nil); err == nil {
		t.Error("a nil option should be rejected")
	}
	// Zero means "the default", not "disabled".
	c, err := typesafecache.New(typesafecache.WithTTL(0), typesafecache.WithMaxEntries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil")
	}
}
