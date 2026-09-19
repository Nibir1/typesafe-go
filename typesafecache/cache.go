// Package typesafecache caches System One responses.
//
// Only input tokens are billed, and a TypeSafe request carries the whole state
// on every call, so repeated evaluation of the same content is the single
// largest avoidable cost in a typical integration — a moderation or triage
// pipeline that sees the same document twice pays twice for an answer that
// cannot have changed.
//
//	cache, err := typesafecache.New(
//	    typesafecache.WithTTL(10*time.Minute),
//	    typesafecache.WithMaxEntries(10_000),
//	)
//	client, err := typesafe.NewClient(
//	    typesafe.WithInterceptor(cache.Interceptor()),
//	)
//
// # Correctness before saving money
//
// Jev is documented as highly consistent, but it is not contractually
// deterministic, and `jev-latest` is an alias that moves without notice. A
// cache keyed on the alias would keep serving answers from the previous model
// version after a move — silently, since nothing in a cached response says
// which model produced it.
//
// The key is therefore built from the **resolved** model id the API reported,
// never the alias that was requested. The alias is tracked separately, learned
// from each response, and when it starts resolving somewhere else every key
// under the old id becomes unreachable at once. That is a clean invalidation
// rather than a slow drift.
//
// The one window this leaves: while a cached alias mapping is still live, a
// move is not noticed. That window is the TTL, which is what a TTL means. Pin
// an exact model id — `jev-1.13.0` rather than `jev-latest` — and the question
// does not arise at all.
//
// # What is not cached
//
// Failures. A cached error is a cached outage: a rate limit or a 500 says
// something about the moment, not about the request, and replaying it after
// the incident is over turns a transient failure into a permanent one.
package typesafecache

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/internal/canonical"
)

// Defaults.
const (
	// DefaultTTL is how long an entry stays fresh.
	//
	// Ten minutes is short enough that an alias move is corrected quickly and
	// long enough to absorb the repeated evaluation a pipeline produces in one
	// working batch.
	DefaultTTL = 10 * time.Minute

	// DefaultMaxEntries bounds the in-memory tier.
	DefaultMaxEntries = 1024
)

// Event is what happened to one lookup, reported to an observer.
type Event struct {
	// Key is the cache key, or "" when the key could not be derived.
	Key string

	// Hit is whether the response came from the cache.
	Hit bool

	// Tier is where it came from: "memory", "disk", or "" on a miss.
	Tier string

	// Model is the resolved model id the key was built from, or "" when the
	// alias had not been resolved yet.
	Model string

	// Alias is the model as the request asked for it, which may be an alias
	// or empty for the client default.
	Alias string

	// AliasMoved is true when this response revealed that the requested alias
	// now resolves to a different model than the cache had recorded. Every
	// key under the previous id became unreachable at that moment.
	AliasMoved bool

	// Err is a cache-internal failure — a disk read that could not be
	// decoded, say. It never fails the request: a broken cache degrades to no
	// cache.
	Err error
}

// Option configures a Cache.
type Option func(*Cache) error

// WithTTL sets how long an entry stays fresh. Zero means DefaultTTL.
//
// It also bounds how long a moved model alias can go unnoticed, so a long TTL
// against `jev-latest` is a deliberate trade, not a free saving.
func WithTTL(d time.Duration) Option {
	return func(c *Cache) error {
		if d < 0 {
			return fmt.Errorf("typesafecache: TTL must not be negative")
		}
		if d == 0 {
			d = DefaultTTL
		}
		c.ttl = d
		return nil
	}
}

// WithMaxEntries bounds the in-memory tier. Zero means DefaultMaxEntries.
func WithMaxEntries(n int) Option {
	return func(c *Cache) error {
		if n < 0 {
			return fmt.Errorf("typesafecache: max entries must not be negative")
		}
		if n == 0 {
			n = DefaultMaxEntries
		}
		c.maxEntries = n
		return nil
	}
}

// WithObserver registers a function called once per lookup.
//
// For hit-rate metrics, and for noticing an alias move: Event.AliasMoved is
// the only signal that the model behind an alias changed under you.
func WithObserver(fn func(Event)) Option {
	return func(c *Cache) error {
		c.observer = fn
		return nil
	}
}

// WithSharedResponses returns the stored response itself on a hit, rather than
// a copy.
//
// The default copies, because a *SystemOneResponse handed to two goroutines is
// shared mutable state and this SDK does not hand those out. The copy is
// shallow over the answer map: the json.RawMessage values are not duplicated,
// since nothing in this SDK writes through them.
//
// Turn copying off when a hit is on a hot path and the responses are treated
// as read-only, which is the normal case. It is the difference between an
// allocation-free hit and one small map allocation per hit.
func WithSharedResponses() Option {
	return func(c *Cache) error {
		c.share = true
		return nil
	}
}

// Cache is a response cache. It is safe for concurrent use.
type Cache struct {
	mu   sync.Mutex
	lru  *lru
	disk *diskTier

	// aliases maps a requested model — possibly an alias, possibly "" for the
	// client default — to the resolved id the API last reported for it.
	//
	// This is what makes an alias move a clean invalidation: entries are keyed
	// on the resolved id, so changing this mapping orphans every key under the
	// old one in a single assignment.
	aliases map[string]aliasEntry

	ttl        time.Duration
	maxEntries int
	share      bool
	closed     bool

	observer func(Event)

	// now is swappable so tests can age a cache without sleeping.
	now func() time.Time

	hits, misses, stores, evictions uint64
}

type aliasEntry struct {
	model string
	at    time.Time
}

// New builds a cache.
func New(opts ...Option) (*Cache, error) {
	c := &Cache{
		aliases:    make(map[string]aliasEntry),
		ttl:        DefaultTTL,
		maxEntries: DefaultMaxEntries,
		now:        time.Now,
	}
	for _, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("typesafecache: nil option")
		}
		if err := o(c); err != nil {
			return nil, err
		}
	}
	c.lru = newLRU(c.maxEntries)
	return c, nil
}

// Interceptor returns the interceptor that serves this cache.
//
//	typesafe.WithInterceptor(cache.Interceptor())
//
// Place it innermost — last in the WithInterceptor list — if you want a hit to
// still be traced and logged by the interceptors outside it. Place it
// outermost to skip that work entirely on a hit. Neither is wrong; they answer
// different questions, and which one you want depends on whether your
// dashboards should show cached calls.
func (c *Cache) Interceptor() typesafe.Interceptor {
	return func(next typesafe.Handler) typesafe.Handler {
		return func(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
			if req == nil {
				return next(ctx, req)
			}

			alias := req.Model
			key, resolved, err := c.keyFor(req)
			if err != nil {
				// A key that cannot be derived is a cache that does not
				// apply, not a request that fails.
				c.report(Event{Alias: alias, Err: err})
				return next(ctx, req)
			}

			if key != "" {
				if resp, tier, ok := c.get(key); ok {
					c.report(Event{Key: key, Hit: true, Tier: tier, Model: resolved, Alias: alias})
					return resp, nil
				}
			} else {
				// No key could be derived, so nothing could be looked up —
				// but the caller still paid for a live request, which is what
				// a miss means to anyone reading the hit rate. Not counting
				// it would report a 100% hit rate on a cache that never hit.
				c.countMiss()
			}

			resp, err := next(ctx, req)
			if err != nil || resp == nil {
				// Failures are never cached: a rate limit or a 500 describes
				// the moment, not the request.
				c.report(Event{Key: key, Alias: alias, Model: resolved, Err: nil})
				return resp, err
			}

			moved := c.learnAlias(alias, resp.Model)
			storeKey, err := c.keyForModel(resp.Model, req)
			if err != nil {
				// The call succeeded; only the storing failed. Returning the
				// error here would fail a request that worked, which is the
				// one thing a cache must never do — a broken cache degrades
				// to no cache, not to an outage.
				c.report(Event{Alias: alias, Model: resp.Model, AliasMoved: moved, Err: err})
				return resp, nil //nolint:nilerr // deliberate: see above.
			}
			c.put(storeKey, resp)
			c.report(Event{Key: storeKey, Alias: alias, Model: resp.Model, AliasMoved: moved})
			return resp, nil
		}
	}
}

// keyFor derives the lookup key for a request, using the resolved model the
// cache has recorded for its alias.
//
// Returns an empty key, and no error, when the alias has not been resolved
// yet: the very first request for a model cannot hit, because nothing yet
// knows which model it will reach.
func (c *Cache) keyFor(req *typesafe.SystemOneRequest) (key, resolved string, err error) {
	c.mu.Lock()
	entry, ok := c.aliases[req.Model]
	expired := ok && c.now().Sub(entry.at) > c.ttl
	c.mu.Unlock()

	if !ok || expired {
		// An expired alias mapping forces one live request, which is what
		// re-checks where the alias points.
		return "", "", nil
	}
	key, err = c.keyForModel(entry.model, req)
	return key, entry.model, err
}

// keyForModel hashes the request against an explicit resolved model id.
//
// The hash covers the model, the state and the questions — everything that can
// change an answer. It uses the same canonical form as the cassette matcher,
// so a cache key and a cassette key agree about what "the same request" means.
func (c *Cache) keyForModel(model string, req *typesafe.SystemOneRequest) (string, error) {
	return canonical.Hash(struct {
		Model     string                       `json:"model"`
		State     any                          `json:"state"`
		Questions map[string]typesafe.Question `json:"questions"`
	}{model, req.State, req.Questions})
}

// learnAlias records where an alias resolved, and reports whether it moved.
func (c *Cache) learnAlias(alias, resolved string) bool {
	if resolved == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	prev, had := c.aliases[alias]
	c.aliases[alias] = aliasEntry{model: resolved, at: c.now()}
	return had && prev.model != resolved
}

// get looks up a key in memory, then on disk.
func (c *Cache) get(key string) (*typesafe.SystemOneResponse, string, bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, "", false
	}
	resp, ok := c.lru.get(key, c.now(), c.ttl)
	if ok {
		c.hits++
		share := c.share
		c.mu.Unlock()
		if share {
			return resp, "memory", true
		}
		return copyResponse(resp), "memory", true
	}
	disk := c.disk
	c.mu.Unlock()

	if disk == nil {
		c.countMiss()
		return nil, "", false
	}

	resp, err := disk.get(key, c.now(), c.ttl)
	if err != nil || resp == nil {
		if err != nil {
			c.report(Event{Key: key, Err: err})
		}
		c.countMiss()
		return nil, "", false
	}

	// Promote to memory: a disk hit that stays on disk pays the decode cost
	// every time.
	c.mu.Lock()
	c.hits++
	c.lru.put(key, resp, c.now())
	share := c.share
	c.mu.Unlock()

	if share {
		return resp, "disk", true
	}
	return copyResponse(resp), "disk", true
}

func (c *Cache) countMiss() {
	c.mu.Lock()
	c.misses++
	c.mu.Unlock()
}

// put stores a response in memory, and on disk when a disk tier is configured.
func (c *Cache) put(key string, resp *typesafe.SystemOneResponse) {
	// Store a copy, so a caller mutating the response it was handed cannot
	// change what the next lookup returns.
	stored := copyResponse(resp)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	evicted := c.lru.put(key, stored, c.now())
	c.stores++
	c.evictions += uint64(evicted)
	disk := c.disk
	c.mu.Unlock()

	if disk != nil {
		if err := disk.put(key, stored, c.now()); err != nil {
			c.report(Event{Key: key, Err: err})
		}
	}
}

func (c *Cache) report(e Event) {
	if c.observer != nil {
		c.observer(e)
	}
}

// Stats is a snapshot of cache activity.
type Stats struct {
	Hits, Misses, Stores, Evictions uint64

	// Entries is how many live entries the memory tier holds.
	Entries int
}

// HitRate is hits over lookups, or 0 when there have been none.
func (s Stats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// Stats returns a snapshot.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Hits:      c.hits,
		Misses:    c.misses,
		Stores:    c.stores,
		Evictions: c.evictions,
		Entries:   c.lru.len(),
	}
}

// Purge drops every cached response from the memory tier. The disk tier is
// left alone; use PurgeDisk for that.
//
// What an alias resolves to is deliberately *not* forgotten. It is knowledge
// about the API rather than a cached answer, and dropping it would mean the
// next response has nothing to compare against — so a model alias that moved
// while the cache was empty would go unreported, which is the one thing this
// cache is built to notice. Use ForgetAliases when you want that state gone
// too.
func (c *Cache) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.purge()
}

// ForgetAliases drops what the cache learned about where model aliases
// resolve, so the next request for each one re-learns it.
//
// Rarely needed. Reach for it when a process is long-lived enough that the
// recorded mapping should not be trusted across a deployment boundary.
func (c *Cache) ForgetAliases() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.aliases = make(map[string]aliasEntry)
}

// PurgeDisk removes every file in the disk tier. It is a no-op without one.
func (c *Cache) PurgeDisk() error {
	c.mu.Lock()
	disk := c.disk
	c.mu.Unlock()
	if disk == nil {
		return nil
	}
	return disk.purge()
}

// Close releases the cache. Lookups after it are misses, and stores are
// dropped, so a closed cache degrades to no cache rather than to an error.
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.lru.purge()
	return nil
}

// copyResponse returns a shallow copy with its own answer map.
//
// The json.RawMessage values are shared: they are never written through by
// this SDK, and duplicating them would make every hit allocate proportionally
// to the response size for no benefit.
func copyResponse(r *typesafe.SystemOneResponse) *typesafe.SystemOneResponse {
	if r == nil {
		return nil
	}
	out := *r
	if r.Answers != nil {
		out.Answers = make(map[string]json.RawMessage, len(r.Answers))
		for k, v := range r.Answers {
			out.Answers[k] = v
		}
	}
	return &out
}
