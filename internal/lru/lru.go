// Package lru implements cache with least recent used eviction policy.
//
// LRU is not safe for concurrent use on its own: it is only ever driven
// from under the TwoQueue lock, which must cover a whole 2Q operation
// spanning several queues, so a second lock here would be pure overhead.
package lru

import "github.com/d1n-go/2q/internal/ring"

// LRU implements a cache with least recent used eviction policy.
type LRU[K comparable, V any] struct {
	r    *ring.Ring[K, V]
	size int
}

// Evicted holds key/value pair that was evicted from cache.
type Evicted[K comparable, V any] struct {
	Key   K
	Value V
}

// Get returns pointer to value for key, if value was in cache (nil returned otherwise).
func (L *LRU[K, V]) Get(key K) *V {
	if i, ok := L.r.Find(key); ok {
		L.r.MoveToFront(i)
		return L.r.Value(i)
	}

	return nil
}

// Set inserts key value pair and returns evicted value, if cache was full.
// If cache size is less than 1 – method will always return reference to value (as if it was immediately evicted).
func (L *LRU[K, V]) Set(key K, value V) *Evicted[K, V] {
	if L.size < 1 {
		return &Evicted[K, V]{key, value}
	}

	if i, ok := L.r.Find(key); ok {
		previousValue := L.r.Value(i)
		L.r.MoveToFront(i)
		L.r.SetValue(i, &value)
		return &Evicted[K, V]{key, *previousValue}
	}

	i := L.r.Back()
	evictedKey, evictedValue := L.r.Evict(i, key, &value)
	if evictedValue != nil {
		return &Evicted[K, V]{evictedKey, *evictedValue}
	}
	return nil
}

// Purge removes every entry. Capacity is unchanged.
func (L *LRU[K, V]) Purge() {
	L.r.Reset()
}

// Contains reports whether key is cached, without modifying its recency.
func (L *LRU[K, V]) Contains(key K) bool {
	_, ok := L.r.Find(key)
	return ok
}

// AppendKeys appends the cached keys to dst, most to least recently used, and returns the extended slice.
func (L *LRU[K, V]) AppendKeys(dst []K) []K {
	return L.r.AppendKeys(dst)
}

// Len returns number of cached items.
func (L *LRU[K, V]) Len() int {
	return L.r.Len()
}

// Remove method removes entry associated with key and returns pointer to removed value (or nil if entry was not in cache).
func (L *LRU[K, V]) Remove(key K) *V {
	if i, ok := L.r.Find(key); ok {
		value := L.r.Value(i)
		L.r.Remove(i)
		return value
	}

	return nil
}

// Peek returns value for key (if key was in cache), but does not modify its recency.
func (L *LRU[K, V]) Peek(key K) *V {
	if i, ok := L.r.Find(key); ok {
		return L.r.Value(i)
	}

	return nil
}

// Victim returns pointer to a key, that will be evicted on next Set call (or nil if there is a space for another key).
// If cache size is 0 - nil will be always returned.
func (L *LRU[K, V]) Victim() *K {
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

// New creates LRU cache with size capacity. Cache will preallocate size count of internal structures to avoid allocation in process.
func New[K comparable, V any](size int) *LRU[K, V] {
	return &LRU[K, V]{
		r:    ring.New[K, V](size),
		size: size,
	}
}
