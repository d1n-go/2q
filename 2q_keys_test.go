package twoqueue

import (
	"slices"
	"testing"
)

func TestContains(t *testing.T) {
	l := NewParams[int, int](2, 2, 2)
	l.Set(1, 1)
	l.Set(2, 2)
	l.Set(3, 3) // 1 -> ghost

	if !l.Contains(2) || !l.Contains(3) {
		t.Fatal("keys in recent must be reported")
	}
	if l.Contains(1) {
		t.Fatal("a key that only lives in the ghost queue is not cached")
	}
	if l.Contains(9) {
		t.Fatal("unknown key reported as cached")
	}

	l.Set(1, 1) // promoted to frequent
	if !l.Contains(1) {
		t.Fatal("key in frequent must be reported")
	}

	// Contains must not touch recency. Fill frequent so that 1 is its LRU
	// entry, probe it, and check that the next promotion still evicts 1.
	l.Set(4, 4) // recent [4 3], 2 -> ghost
	l.Set(2, 2) // 2 -> frequent: [2 1], 1 is the LRU victim
	l.Contains(1)
	if e := l.Set(5, 5); e == nil || e.Key != 3 {
		t.Fatalf("recent should evict 3, got %+v", e)
	}
	l.Set(3, 3) // 3 -> frequent, evicting its LRU entry
	if l.Contains(1) {
		t.Fatal("Contains must not refresh recency: 1 should have been the LRU victim")
	}
}

func TestKeys(t *testing.T) {
	l := NewParams[int, int](3, 3, 3)

	if ks := l.Keys(); len(ks) != 0 {
		t.Fatalf("empty cache: %v", ks)
	}

	// recent newest->oldest is [3 2 1]
	l.Set(1, 1)
	l.Set(2, 2)
	l.Set(3, 3)
	if got, want := l.Keys(), []int{3, 2, 1}; !slices.Equal(got, want) {
		t.Fatalf("Keys=%v want %v", got, want)
	}

	// evict 1 and 2 into the ghost queue, promote both: frequent MRU->LRU is [2 1]
	l.Set(4, 4)
	l.Set(5, 5)
	l.Set(1, 1)
	l.Set(2, 2)
	if got, want := l.Keys(), []int{2, 1, 5, 4, 3}; !slices.Equal(got, want) {
		t.Fatalf("Keys=%v want frequent then recent %v", got, want)
	}

	// Get refreshes recency in frequent; Keys itself must not.
	l.Get(1)
	if got, want := l.Keys(), []int{1, 2, 5, 4, 3}; !slices.Equal(got, want) {
		t.Fatalf("Keys after Get(1)=%v want %v", got, want)
	}
	if got, want := l.Keys(), []int{1, 2, 5, 4, 3}; !slices.Equal(got, want) {
		t.Fatalf("Keys is not idempotent: %v want %v", got, want)
	}

	// The snapshot is the caller's to keep.
	ks := l.Keys()
	ks[0] = 99
	if l.Keys()[0] != 1 {
		t.Fatal("modifying the returned slice must not affect the cache")
	}

	l.Remove(5)
	if got, want := l.Keys(), []int{1, 2, 4, 3}; !slices.Equal(got, want) {
		t.Fatalf("Keys after Remove(5)=%v want %v", got, want)
	}
	if got := l.Keys(); len(got) != l.Len() {
		t.Fatalf("len(Keys)=%d != Len()=%d", len(got), l.Len())
	}
}
