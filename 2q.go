package twoqueue

import (
	"fmt"
	"sync"

	"github.com/d1n-go/2q/internal/fifo"
	"github.com/d1n-go/2q/internal/lru"
	"github.com/d1n-go/2q/internal/ring"
)

const (
	// Default2QRecentRatio is the ratio of the 2Q cache dedicated
	// to recently added entries that have only been accessed once.
	Default2QRecentRatio = 0.25

	// Default2QGhostEntries is the default ratio of ghost
	// entries kept to track entries recently evicted
	Default2QGhostEntries = 0.50
)

// TwoQueue is a thread-safe fixed size 2Q cache.
// 2Q is an enhancement over the standard LRU cache
// in that it tracks both frequently and recently used
// entries separately. This avoids a burst in access to new
// entries from evicting frequently used entries. It adds some
// additional tracking overhead to the standard LRU cache, and is
// computationally about 2x the cost, and adds some metadata over
// head.
//
// All methods take a single cache-wide lock: every operation touches up
// to three internal queues, and the promotion logic in Set is a
// check-then-act across them, so per-queue locking alone would let two
// concurrent writers of the same key leave a copy of it in both recent
// and frequent.
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
// index entry and inflates Len. Do not use NaN as a key.
type TwoQueue[K comparable, V any] struct {
	mu          sync.Mutex
	recent      *fifo.FIFO[K, V]        // A1in in paper
	recentEvict *fifo.FIFO[K, struct{}] // A1out in paper
	frequent    *lru.LRU[K, V]          // Am in paper
}

// Evicted holds key/value pair that was evicted from cache.
type Evicted[K comparable, V any] struct {
	Key   K
	Value V
}

func fromLruEvicted[K comparable, V any](e *lru.Evicted[K, V]) *Evicted[K, V] {
	if e == nil {
		return nil
	}

	return &Evicted[K, V]{
		e.Key,
		e.Value,
	}
}

func fromFifoEvicted[K comparable, V any](e *fifo.Evicted[K, V]) *Evicted[K, V] {
	if e == nil {
		return nil
	}

	return &Evicted[K, V]{
		e.Key,
		e.Value,
	}
}

// Get probes frequent and recent cached items and returns pointer to value (or nil if it was not found).
// A hit in the frequent queue marks the entry as most recently used.
func (L *TwoQueue[K, V]) Get(key K) *V {
	L.mu.Lock()
	defer L.mu.Unlock()

	if e := L.frequent.Get(key); e != nil {
		return e
	}

	return L.recent.Get(key)
}

// Set stores key/value pair in 2Q cache following 2Q Full Version promotion algorytm.
func (L *TwoQueue[K, V]) Set(key K, value V) *Evicted[K, V] {
	L.mu.Lock()
	defer L.mu.Unlock()

	if e := L.frequent.Peek(key); e != nil {
		return fromLruEvicted(L.frequent.Set(key, value))
	}

	if L.recentEvict.Get(key) != nil {
		L.recentEvict.Remove(key)
		return fromLruEvicted(L.frequent.Set(key, value))
	}

	if re := L.recent.Push(key, value); re != nil {
		L.recentEvict.Push(re.Key, struct{}{})
		return fromFifoEvicted(re)
	}

	return nil
}

// Purge removes every entry from the cache, including the ghost keys
// that track recent evictions, so the cache behaves as freshly created.
// Capacity is unchanged and the preallocated storage is kept.
func (L *TwoQueue[K, V]) Purge() {
	L.mu.Lock()
	defer L.mu.Unlock()

	L.frequent.Purge()
	L.recentEvict.Purge()
	L.recent.Purge()
}

// Len returns size of cache (frequent + recent items)
func (L *TwoQueue[K, V]) Len() int {
	L.mu.Lock()
	defer L.mu.Unlock()

	return L.frequent.Len() + L.recent.Len()
}

// Peek returns value for key (if key was in cache), but does not modify its recency.
func (L *TwoQueue[K, V]) Peek(key K) *V {
	L.mu.Lock()
	defer L.mu.Unlock()

	if e := L.frequent.Peek(key); e != nil {
		return e
	}

	return L.recent.Get(key)
}

// Remove method removes entry associated with key and returns pointer to removed value (or nil if entry was not in cache).
func (L *TwoQueue[K, V]) Remove(key K) *V {
	L.mu.Lock()
	defer L.mu.Unlock()

	if e := L.frequent.Remove(key); e != nil {
		return e
	}

	return L.recent.Remove(key)
}

// MaxSize is the largest capacity accepted for any single queue by
// NewParams (and therefore the largest size accepted by New).
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
// Cache will preallocate size count of internal structures to avoid allocation in process.
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
	return &TwoQueue[K, V]{
		recent:      fifo.New[K, V](clampSize(Kin)),
		recentEvict: fifo.New[K, struct{}](clampSize(Kout)),
		frequent:    lru.New[K, V](clampSize(size)),
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
