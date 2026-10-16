// Package ring implements a fixed-capacity intrusive ring of key/value
// slots backed by a single slice. It replaces the container/list-based
// approach of the upstream lru and fifo packages (a doubly linked list of
// heap-allocated elements): all node storage is preallocated once in New,
// and links are plain slice indices instead of pointers.
//
// A Ring does not index its keys. The caller keeps one index for all of
// its rings, mapping a key to the ring and slot that hold it, and must
// update it from what Evict and Remove report.
package ring

import (
	"fmt"
	"math"
)

// MaxSize is the largest ring New accepts: slots, and the sentinel that
// sits at index size, are addressed by int32.
const MaxSize = math.MaxInt32

type node[K comparable, V any] struct {
	key        K
	value      *V
	prev, next int32
}

// Ring holds size real slots (nodes[0:size]) linked into a circular list
// around a sentinel node at index root, mirroring the structure of
// container/list: root.next is the front (most recently used/inserted)
// slot and root.prev is the back (eviction victim) slot.
//
// A slot is occupied while its value is non-nil; empty slots always sit
// together at the back, so Back is empty exactly when the ring has room.
type Ring[K comparable, V any] struct {
	nodes []node[K, V]
	root  int32
	len   int
}

// New creates a Ring with size preallocated, empty slots. It panics if
// size is negative or exceeds MaxSize; callers with a looser contract
// (the public constructors clamp negatives to zero) must normalize first.
func New[K comparable, V any](size int) *Ring[K, V] {
	if size < 0 || size > MaxSize {
		panic(fmt.Sprintf("ring: size %d out of range [0, %d]", size, MaxSize))
	}

	r := &Ring[K, V]{
		nodes: make([]node[K, V], size+1),
		root:  int32(size),
	}
	r.Reset()

	return r
}

// Reset empties the ring: every slot is zeroed (dropping the keys and
// values it held) and relinked in slot order behind the sentinel.
// Capacity is unchanged.
func (r *Ring[K, V]) Reset() {
	clear(r.nodes)
	r.len = 0

	size := r.root
	if size == 0 {
		r.nodes[r.root].prev = r.root
		r.nodes[r.root].next = r.root
		return
	}

	for i := range size {
		prev, next := i-1, i+1
		if i == 0 {
			prev = r.root
		}
		if i == size-1 {
			next = r.root
		}
		r.nodes[i].prev = prev
		r.nodes[i].next = next
	}
	r.nodes[r.root].next = 0
	r.nodes[r.root].prev = size - 1
}

// Cap returns the number of slots.
func (r *Ring[K, V]) Cap() int {
	return int(r.root)
}

// Key returns the key currently stored in slot i.
func (r *Ring[K, V]) Key(i int32) K {
	return r.nodes[i].key
}

// Value returns the value pointer currently stored in slot i (nil for an
// empty slot).
func (r *Ring[K, V]) Value(i int32) *V {
	return r.nodes[i].value
}

// SetValue replaces the value stored in slot i, keeping its key and
// position unchanged.
func (r *Ring[K, V]) SetValue(i int32, v *V) {
	r.nodes[i].value = v
}

// Back returns the index of the back (least recently used/oldest) slot.
func (r *Ring[K, V]) Back() int32 {
	return r.nodes[r.root].prev
}

func (r *Ring[K, V]) move(e, at int32) {
	if e == at {
		return
	}

	r.nodes[r.nodes[e].prev].next = r.nodes[e].next
	r.nodes[r.nodes[e].next].prev = r.nodes[e].prev

	r.nodes[e].prev = at
	r.nodes[e].next = r.nodes[at].next
	r.nodes[r.nodes[e].prev].next = e
	r.nodes[r.nodes[e].next].prev = e
}

// MoveToFront moves slot e to the front of the ring.
func (r *Ring[K, V]) MoveToFront(e int32) {
	if r.nodes[r.root].next == e {
		return
	}
	r.move(e, r.root)
}

// MoveToBack moves slot e to the back of the ring.
func (r *Ring[K, V]) MoveToBack(e int32) {
	if r.nodes[r.root].prev == e {
		return
	}
	r.move(e, r.nodes[r.root].prev)
}

// Evict installs key/value (value must be non-nil) in slot i, moves it to
// the front, and returns what the slot held before. evictedValue is nil
// if the slot was empty: evictedKey is then just K's zero value, which
// may coincide with a real key stored elsewhere, so the caller must drop
// evictedKey from its index only when evictedValue is non-nil. (Upstream
// deleted it unconditionally; that was its data-loss bug.)
func (r *Ring[K, V]) Evict(i int32, key K, value *V) (evictedKey K, evictedValue *V) {
	evictedKey, evictedValue = r.nodes[i].key, r.nodes[i].value
	if evictedValue == nil {
		r.len++
	}

	r.nodes[i].key = key
	r.nodes[i].value = value
	r.MoveToFront(i)

	return evictedKey, evictedValue
}

// Remove empties occupied slot i and moves it to the back so it is the
// next one reused. The key is zeroed as well so that a removed entry does
// not keep its key (and whatever that key references) alive until the
// slot is recycled.
func (r *Ring[K, V]) Remove(i int32) {
	var zero K
	r.nodes[i].key = zero
	r.nodes[i].value = nil
	r.len--
	r.MoveToBack(i)
}

// AppendKeys appends the keys of every occupied slot to dst, walking
// from the front (most recently used/inserted) to the back, and returns
// the extended slice.
func (r *Ring[K, V]) AppendKeys(dst []K) []K {
	for i := r.nodes[r.root].next; i != r.root; i = r.nodes[i].next {
		if r.nodes[i].value != nil {
			dst = append(dst, r.nodes[i].key)
		}
	}
	return dst
}

// Len returns the number of occupied slots.
func (r *Ring[K, V]) Len() int {
	return r.len
}
