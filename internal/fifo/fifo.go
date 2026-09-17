// Package fifo implements a fixed size FIFO with O(1) Get.
//
// FIFO is not safe for concurrent use on its own: it is only ever driven
// from under the TwoQueue lock, which must cover a whole 2Q operation
// spanning several queues, so a second lock here would be pure overhead.
package fifo

import "github.com/d1n-go/2q/internal/ring"

// FIFO implements a fixed size FIFO with O(1) Get.
type FIFO[K comparable, V any] struct {
	r    *ring.Ring[K, V]
	size int
}

// Evicted holds key/value pair that was evicted from cache.
type Evicted[K comparable, V any] struct {
	Key   K
	Value V
}

// Get returns pointer to value for key, if value was in cache (nil returned otherwise).
// Unlike LRU, Get does not affect the eviction order.
func (L *FIFO[K, V]) Get(key K) *V {
	if i, ok := L.r.Find(key); ok {
		return L.r.Value(i)
	}

	return nil
}

// Push inserts key value pair and returns evicted value, if cache was full.
// If cache size is less than 1 – method will always return reference to value (as if it was immediately evicted).
func (L *FIFO[K, V]) Push(key K, value V) *Evicted[K, V] {
	if L.size < 1 {
		return &Evicted[K, V]{key, value}
	}

	if i, ok := L.r.Find(key); ok {
		L.r.SetValue(i, &value)
		return nil
	}

	i := L.r.Back()
	evictedKey, evictedValue := L.r.Evict(i, key, &value)
	if evictedValue != nil {
		return &Evicted[K, V]{evictedKey, *evictedValue}
	}
	return nil
}

// Len returns number of cached items.
func (L *FIFO[K, V]) Len() int {
	return L.r.Len()
}

// Remove method removes entry associated with key and returns pointer to removed value (or nil if entry was not in cache).
func (L *FIFO[K, V]) Remove(key K) *V {
	if i, ok := L.r.Find(key); ok {
		value := L.r.Value(i)
		L.r.MoveToBack(i)
		L.r.SetValue(i, nil)
		L.r.DeleteIndex(key)
		return value
	}

	return nil
}

// Victim returns pointer to a key, that will be evicted on next Push call (or nil if there is a space for another key).
// If cache size is 0 - nil will be always returned.
func (L *FIFO[K, V]) Victim() *K {
	if L.size < 1 {
		return nil
	}

	i := L.r.Back()
	if v := L.r.Value(i); v != nil {
		k := L.r.Key(i)
		return &k
	}

	return nil
}

// New creates FIFO cache with size capacity. Cache will preallocate size count of internal structures to avoid allocation in process.
func New[K comparable, V any](size int) *FIFO[K, V] {
	return &FIFO[K, V]{
		r:    ring.New[K, V](size),
		size: size,
	}
}
