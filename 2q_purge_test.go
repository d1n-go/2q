package twoqueue

import "testing"

func TestPurge(t *testing.T) {
	l := NewParams[int, string](2, 2, 2)

	// Populate every queue: recent, ghost (via eviction) and frequent (via promotion).
	l.Set(1, "a")
	l.Set(2, "b")
	l.Set(3, "c") // evicts 1 into the ghost queue
	l.Set(1, "a") // promotes 1 into frequent
	l.Set(4, "d") // evicts 2 into the ghost queue
	if l.Len() != 3 || l.recentEvict.Len() != 1 {
		t.Fatalf("setup: Len=%d ghosts=%d", l.Len(), l.recentEvict.Len())
	}

	l.Purge()

	if n := l.Len(); n != 0 {
		t.Fatalf("Len after Purge = %d, want 0", n)
	}
	for _, k := range []int{1, 2, 3, 4} {
		if v := l.Get(k); v != nil {
			t.Fatalf("key %d still cached after Purge: %q", k, *v)
		}
	}
	if n := l.recentEvict.Len(); n != 0 {
		t.Fatalf("ghost queue still holds %d keys after Purge", n)
	}

	// A key that was in the ghost queue must not be promoted straight to
	// frequent any more: the cache has to behave as freshly created.
	if e := l.Set(2, "b"); e != nil {
		t.Fatalf("first Set after Purge should land in recent without eviction, got %+v", e)
	}
	if l.recent.Len() != 1 || l.frequent.Len() != 0 {
		t.Fatalf("key 2 should be in recent, not frequent: recent=%d frequent=%d", l.recent.Len(), l.frequent.Len())
	}

	// Capacity is retained: recent still overflows at exactly Kin entries.
	l.Set(5, "e")
	if e := l.Set(6, "f"); e == nil || e.Key != 2 {
		t.Fatalf("recent should still hold exactly 2 entries after Purge, got %+v", e)
	}
}

func TestPurge_ZeroCapacity(t *testing.T) {
	l := NewParams[int, int](0, 0, 0)
	l.Set(1, 1)
	l.Purge()
	if l.Len() != 0 || l.Get(1) != nil {
		t.Fatal("zero-capacity cache must survive Purge")
	}
}
