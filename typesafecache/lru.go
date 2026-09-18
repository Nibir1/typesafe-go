package typesafecache

import (
	"container/list"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
)

// lru is a bounded least-recently-used map with per-entry expiry.
//
// container/list rather than a hand-rolled list: it is stdlib, it is the same
// intrusive doubly-linked list every LRU needs, and writing a third one here
// would add bugs, not value. Every method assumes the caller holds the
// Cache's mutex — the lock lives one level up because a lookup also touches
// the alias map and the counters, and two locks around one operation is how
// deadlocks are written.
type lru struct {
	max   int
	ll    *list.List               // front is most recently used
	items map[string]*list.Element // key -> element holding *entry
}

type entry struct {
	key   string
	resp  *typesafe.SystemOneResponse
	added time.Time
}

func newLRU(max int) *lru {
	if max < 1 {
		max = 1
	}
	return &lru{
		max:   max,
		ll:    list.New(),
		items: make(map[string]*list.Element, max),
	}
}

// get returns a live entry and marks it most recently used.
//
// An expired entry is removed rather than merely reported missing, so a cache
// full of stale keys does not evict live ones to make room for them.
func (l *lru) get(key string, now time.Time, ttl time.Duration) (*typesafe.SystemOneResponse, bool) {
	el, ok := l.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*entry)
	if now.Sub(e.added) > ttl {
		l.removeElement(el)
		return nil, false
	}
	l.ll.MoveToFront(el)
	return e.resp, true
}

// put stores a response, evicting from the back as needed. It returns how many
// entries were evicted.
func (l *lru) put(key string, resp *typesafe.SystemOneResponse, now time.Time) int {
	if el, ok := l.items[key]; ok {
		e := el.Value.(*entry)
		e.resp = resp
		e.added = now
		l.ll.MoveToFront(el)
		return 0
	}

	l.items[key] = l.ll.PushFront(&entry{key: key, resp: resp, added: now})

	evicted := 0
	for l.ll.Len() > l.max {
		if back := l.ll.Back(); back != nil {
			l.removeElement(back)
			evicted++
		}
	}
	return evicted
}

func (l *lru) removeElement(el *list.Element) {
	l.ll.Remove(el)
	delete(l.items, el.Value.(*entry).key)
}

func (l *lru) len() int { return l.ll.Len() }

func (l *lru) purge() {
	l.ll.Init()
	clear(l.items)
}
