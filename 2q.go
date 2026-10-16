// Package twoqueue implements a thread-safe, fixed-size, generic 2Q cache
// (Johnson & Shasha, "2Q: A Low Overhead High Performance Buffer
// Management Replacement Algorithm", VLDB 1994).
//
// A 2Q cache keeps keys seen once in a FIFO (A1in), remembers the keys it
// pages out of it in a ghost FIFO that holds no values (A1out), and
// promotes a key into an LRU (Am) only when it is requested again while
// still remembered there. A burst of one-off keys therefore cycles through
// A1in without displacing the frequently used entries in Am.
//
// The import path is github.com/d1n-go/2q; the package name is twoqueue
// because an identifier cannot start with a digit:
//
//	import twoqueue "github.com/d1n-go/2q"
//
//	cache := twoqueue.New[string, int](1024)
//	cache.Set("answer", 42)
//	if v := cache.Get("answer"); v != nil {
//		fmt.Println(*v)
//	}
//
// Every method takes a single cache-wide mutex, Get included (it updates
// recency), so throughput does not scale with the number of goroutines
// hitting one cache. Under heavy contention, shard keys across several
// caches.
package twoqueue

import (
	"fmt"
	"sync"

	"github.com/d1n-go/2q/internal/ring"
)

const (
	// Default2QRecentRatio is the ratio of the 2Q cache dedicated
	// to recently added entries that have only been accessed once.
	Default2QRecentRatio = 0.25

	// Default2QGhostEntries is the default ratio of ghost
	// entries kept to track entries recently evicted.
	Default2QGhostEntries = 0.50
)

// TwoQueue is a thread-safe fixed size 2Q cache.
// 2Q is an enhancement over the standard LRU cache
// in that it tracks both frequently and recently used
// entries separately. This avoids a burst in access to new
// entries from evicting frequently used entries. It adds some
// additional tracking overhead to the standard LRU cache, and is
// computationally about 2x the cost, and adds some metadata
// overhead.
//
// All methods take a single cache-wide lock: the three queues share one
// key index, and Set moves keys between them, so every operation must see
// and leave all of them consistent.
//
// Get, Peek and Remove return a pointer to the value stored in the cache
// rather than a copy. Writing through that pointer is visible to every
// other reader of the same entry and is not synchronized by the cache
// lock; treat the pointee as read-only, or copy it, unless V itself is
// safe for concurrent mutation.
//
// A TwoQueue must be created with New or NewParams; the zero value is
// not usable and its methods will panic with a nil dereference.
//
// Keys are stored in a Go map, so K must be comparable at run time as
// well as at compile time. With an interface key type such as any, a
// key whose dynamic type is not comparable (a slice, map or func) makes
// the operation panic with "hash of unhashable type", exactly as a map
// access would. Floating-point NaN keys are comparable but never equal
// to themselves: a NaN entry can never be found again, and since a map
// entry keyed by NaN cannot be deleted, every Set of a NaN key leaks an
// index entry (the slot it took is recycled normally). Do not use NaN as
// a key.
type TwoQueue[K comparable, V any] struct {
	mu sync.Mutex
	// index maps every key held by any of the three queues to where it
	// lives. A key is in at most one queue, and every occupied slot has an
	// entry here pointing back at it.
	index       map[K]location
	recent      *ring.Ring[K, V]        // A1in in paper: FIFO
	recentEvict *ring.Ring[K, struct{}] // A1out in paper: ghost FIFO of keys
	frequent    *ring.Ring[K, V]        // Am in paper: LRU
	stats       Stats
}

type queue uint8

const (
	inRecent queue = iota
	inRecentEvict
	inFrequent
)

type location struct {
	queue queue
	slot  int32
}

// ghost is the value stored in every occupied A1out slot; Ring treats a
// nil value as an empty slot. Pointers to zero-size values do not allocate.
var ghost = &struct{}{}

// Stats holds the cache's hit/miss counters. Only Get is counted: Peek,
// Contains and Set are not lookups on behalf of a consumer, and a Get on
// a key that is only remembered in the ghost queue is a miss.
type Stats struct {
	Hits   uint64
	Misses uint64
}

// HitRate returns Hits / (Hits + Misses), or 0 if nothing was looked up.
func (s Stats) HitRate() float64 {
	if total := s.Hits + s.Misses; total > 0 {
		return float64(s.Hits) / float64(total)
	}
	return 0
}

// Evicted holds key/value pair that was evicted from cache.
type Evicted[K comparable, V any] struct {
	Key   K
	Value V
}

// Get probes frequent and recent cached items and returns pointer to value (or nil if it was not found).
// A hit in the frequent queue marks the entry as most recently used.
func (L *TwoQueue[K, V]) Get(key K) *V {
	L.mu.Lock()
	defer L.mu.Unlock()

	var e *V
	if loc, ok := L.index[key]; ok {
		switch loc.queue {
		case inFrequent:
			L.frequent.MoveToFront(loc.slot)
			e = L.frequent.Value(loc.slot)
		case inRecent:
			e = L.recent.Value(loc.slot)
		}
	}

	if e != nil {
		L.stats.Hits++
	} else {
		L.stats.Misses++
	}
	return e
}

// Set stores key/value pair in 2Q cache following 2Q Full Version promotion
// algorithm. It returns the key/value pair the cache stopped holding because
// of this call, or nil if nothing left the cache:
//
//   - Updating a key that is already cached returns that key with its
//     previous value. The key stays cached, now with the new value, and
//     keeps its queue (an update does not promote out of A1in).
//   - Storing a key that does not fit returns the entry evicted to make
//     room: the oldest entry of A1in, or the least recently used one of Am.
//   - With a zero-capacity queue (see NewParams) it can return the key and
//     value just passed in, which were then not stored at all.
//
// A non-nil result therefore always carries a value that is no longer
// reachable through the cache, so it is the place to release whatever that
// value holds. The one caveat is re-setting a key to the value it already
// has: the returned Value is then that same, still cached value.
func (L *TwoQueue[K, V]) Set(key K, value V) *Evicted[K, V] {
	L.mu.Lock()
	defer L.mu.Unlock()

	loc, ok := L.index[key]
	if !ok {
		return L.pushRecent(key, value)
	}

	switch loc.queue {
	case inFrequent:
		previous := *L.frequent.Value(loc.slot)
		L.frequent.MoveToFront(loc.slot)
		L.frequent.SetValue(loc.slot, &value)
		return &Evicted[K, V]{key, previous}
	case inRecent:
		// FIFO: an update keeps the entry's position.
		previous := *L.recent.Value(loc.slot)
		L.recent.SetValue(loc.slot, &value)
		return &Evicted[K, V]{key, previous}
	default: // inRecentEvict: seen again while remembered, promote.
		L.recentEvict.Remove(loc.slot)
		return L.pushFrequent(key, value)
	}
}

// pushRecent inserts a key that is in no queue into A1in, paging the
// oldest A1in entry out to A1out if A1in is full.
func (L *TwoQueue[K, V]) pushRecent(key K, value V) *Evicted[K, V] {
	if L.recent.Cap() == 0 {
		L.pushRecentEvict(key)
		return &Evicted[K, V]{key, value}
	}

	slot := L.recent.Back()
	evictedKey, evictedValue := L.recent.Evict(slot, key, &value)
	if evictedValue != nil {
		L.pushRecentEvict(evictedKey) // re-points (or drops) its index entry
	}
	L.index[key] = location{inRecent, slot}

	if evictedValue != nil {
		return &Evicted[K, V]{evictedKey, *evictedValue}
	}
	return nil
}

// pushRecentEvict remembers key in A1out, forgetting the oldest ghost if
// A1out is full. key may still have an index entry from the queue it just
// left; it is overwritten, or deleted if A1out has no room at all.
func (L *TwoQueue[K, V]) pushRecentEvict(key K) {
	if L.recentEvict.Cap() == 0 {
		delete(L.index, key)
		return
	}

	slot := L.recentEvict.Back()
	if forgotten, was := L.recentEvict.Evict(slot, key, ghost); was != nil {
		delete(L.index, forgotten)
	}
	L.index[key] = location{inRecentEvict, slot}
}

// pushFrequent inserts key into Am, evicting the least recently used
// entry if Am is full. key may still have an index entry pointing at the
// A1out slot it was just removed from; it is overwritten or deleted.
func (L *TwoQueue[K, V]) pushFrequent(key K, value V) *Evicted[K, V] {
	if L.frequent.Cap() == 0 {
		delete(L.index, key)
		return &Evicted[K, V]{key, value}
	}

	slot := L.frequent.Back()
	evictedKey, evictedValue := L.frequent.Evict(slot, key, &value)
	if evictedValue != nil {
		delete(L.index, evictedKey)
	}
	L.index[key] = location{inFrequent, slot}

	if evictedValue != nil {
		return &Evicted[K, V]{evictedKey, *evictedValue}
	}
	return nil
}

// Contains reports whether key is cached, without modifying its recency.
// Keys that are only remembered in the ghost queue are not cached.
func (L *TwoQueue[K, V]) Contains(key K) bool {
	L.mu.Lock()
	defer L.mu.Unlock()

	loc, ok := L.index[key]
	return ok && loc.queue != inRecentEvict
}

// Keys returns a snapshot of the cached keys: the frequent queue from
// most to least recently used, followed by the recent queue from newest
// to oldest. It does not modify recency. The slice is freshly allocated
// and may be modified by the caller.
func (L *TwoQueue[K, V]) Keys() []K {
	L.mu.Lock()
	defer L.mu.Unlock()

	keys := make([]K, 0, L.frequent.Len()+L.recent.Len())
	keys = L.frequent.AppendKeys(keys)
	return L.recent.AppendKeys(keys)
}

// Stats returns a snapshot of the hit/miss counters. They accumulate
// for the lifetime of the cache; Purge does not reset them, ResetStats
// does.
func (L *TwoQueue[K, V]) Stats() Stats {
	L.mu.Lock()
	defer L.mu.Unlock()

	return L.stats
}

// ResetStats zeroes the hit/miss counters and returns their last values.
func (L *TwoQueue[K, V]) ResetStats() Stats {
	L.mu.Lock()
	defer L.mu.Unlock()

	s := L.stats
	L.stats = Stats{}
	return s
}

// Purge removes every entry from the cache, including the ghost keys
// that track recent evictions, so the cache behaves as freshly created.
// Capacity is unchanged and the preallocated storage is kept.
func (L *TwoQueue[K, V]) Purge() {
	L.mu.Lock()
	defer L.mu.Unlock()

	clear(L.index)
	L.frequent.Reset()
	L.recentEvict.Reset()
	L.recent.Reset()
}

// Len returns size of cache (frequent + recent items).
func (L *TwoQueue[K, V]) Len() int {
	L.mu.Lock()
	defer L.mu.Unlock()

	return L.frequent.Len() + L.recent.Len()
}

// Peek returns value for key (if key was in cache), but does not modify its recency.
func (L *TwoQueue[K, V]) Peek(key K) *V {
	L.mu.Lock()
	defer L.mu.Unlock()

	if loc, ok := L.index[key]; ok {
		switch loc.queue {
		case inFrequent:
			return L.frequent.Value(loc.slot)
		case inRecent:
			return L.recent.Value(loc.slot)
		}
	}
	return nil
}

// Remove method removes entry associated with key and returns pointer to removed value (or nil if entry was not in cache).
// A key only remembered in the ghost queue is not cached and stays remembered.
func (L *TwoQueue[K, V]) Remove(key K) *V {
	L.mu.Lock()
	defer L.mu.Unlock()

	loc, ok := L.index[key]
	if !ok || loc.queue == inRecentEvict {
		return nil
	}

	r := L.recent
	if loc.queue == inFrequent {
		r = L.frequent
	}
	v := r.Value(loc.slot)
	r.Remove(loc.slot)
	delete(L.index, key)
	return v
}

// MaxSize is the largest capacity accepted for any single queue by
// NewParams. New panics once its largest split, the Am share of
// 1-Default2QRecentRatio, exceeds it: for sizes above MaxSize/0.75.
const MaxSize = ring.MaxSize

// NewParams creates 2Q cache with specified capacities:
//
// - Kin defines A1in FIFO size for key/value pairs
// - Kout defines A1out FIFO size for keys
// - size defines frequent LRU size for key/value pairs
//
// It's recommended to hold 25% of available memory in Kin. Kout size should correspond
// to 50% memory for values. And size should consume rest of memory. You can refer to
// original paper (http://www.vldb.org/conf/1994/P439.PDF) for computing sizes.
//
// For example, if you can store around 10000 items in cache:
// - Kin should hold around 2500 items.
// - Kout should hold 5000 items.
// - And size should take the rest 7500 items.
//
// The queues' slots and key indexes are preallocated up front, so a full
// cache does not grow. Each Set still allocates a box for the value: that
// is what keeps a pointer returned by Get valid after the entry is updated
// or evicted.
//
// A negative capacity is treated as zero. A zero-capacity queue never
// holds anything: with Kin == 0 every new key is reported as evicted by
// the Set that inserts it and only lands in the cache on the second Set
// (via the ghost queue, if Kout > 0); with size == 0 nothing can be
// held in frequent, so a Set that would promote a key instead reports it
// as evicted immediately and the key is not stored at all.
//
// NewParams panics if any capacity exceeds MaxSize. That is the only
// panic this package raises itself: it signals a programmer error in a
// constant argument, in the spirit of make with a negative length, and
// can never be a run-time condition of a correct program. The cache's
// operations do not panic on a cache built here, whatever the sequence
// of calls; the remaining ways to crash (unhashable interface keys, or a
// capacity whose preallocation exhausts memory) come from the runtime
// and are documented on TwoQueue.
func NewParams[K comparable, V any](Kin int, Kout int, size int) *TwoQueue[K, V] {
	kin, kout, am := clampSize(Kin), clampSize(Kout), clampSize(size)
	return &TwoQueue[K, V]{
		index:       make(map[K]location, kin+kout+am),
		recent:      ring.New[K, V](kin),
		recentEvict: ring.New[K, struct{}](kout),
		frequent:    ring.New[K, V](am),
	}
}

func clampSize(n int) int {
	if n < 0 {
		return 0
	}
	if n > MaxSize {
		panic(fmt.Sprintf("twoqueue: capacity %d exceeds MaxSize (%d)", n, MaxSize))
	}
	return n
}

// New creates 2Q cache with predefined size splits. 25% of size goes to Kin, 50% to KOut and rest to Am size.
//
// The splits are truncated, so sizes below 4 leave Kin at zero and the
// cache only stores keys that are Set at least twice (see NewParams);
// size 1 yields a cache that never stores anything. Sizes of a few
// hundred and up are the intended use.
func New[K comparable, V any](size int) *TwoQueue[K, V] {
	return NewParams[K, V](
		int(Default2QRecentRatio*float64(size)),
		int(Default2QGhostEntries*float64(size)),
		int((1-Default2QRecentRatio)*float64(size)),
	)
}
