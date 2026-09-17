package ring

import (
	"math"
	"testing"
)

func TestRing_zero(t *testing.T) {
	r := New[int, int](0)

	if r.Len() != 0 {
		t.Fatalf("bad len: %v", r.Len())
	}

	if _, ok := r.Find(1); ok {
		t.Fatalf("empty ring should not contain any key")
	}
}

func TestRing_evictEmptySlot(t *testing.T) {
	r := New[int, int](2)

	i := r.Back()
	v := 10
	oldKey, oldValue := r.Evict(i, 1, &v)

	if oldValue != nil {
		t.Fatalf("evicting an empty slot should return a nil value, got %v (key %v)", *oldValue, oldKey)
	}

	if got, ok := r.Find(1); !ok || got != i {
		t.Fatalf("key should now be indexed at slot %v, got %v (found=%v)", i, got, ok)
	}

	if r.Len() != 1 {
		t.Fatalf("bad len: %v", r.Len())
	}
}

func TestRing_evictOccupiedSlot(t *testing.T) {
	r := New[int, int](1)

	v1 := 1
	r.Evict(r.Back(), 1, &v1)

	i := r.Back()
	v2 := 2
	oldKey, oldValue := r.Evict(i, 2, &v2)

	if oldKey != 1 || oldValue == nil || *oldValue != 1 {
		t.Fatalf("expected to evict key=1 value=1, got key=%v value=%v", oldKey, oldValue)
	}

	if _, ok := r.Find(1); ok {
		t.Fatalf("evicted key should no longer be indexed")
	}

	if got, ok := r.Find(2); !ok || got != i {
		t.Fatalf("new key should be indexed at slot %v, got %v (found=%v)", i, got, ok)
	}
}

func TestRing_moveToFrontAndBack(t *testing.T) {
	r := New[int, int](3)

	slots := make([]int32, 3)
	for k := 0; k < 3; k++ {
		i := r.Back()
		v := k
		r.Evict(i, k, &v)
		slots[k] = i
	}

	// Insertion order 0,1,2 each moved to front on insert -> back is now 0.
	if got := r.Key(r.Back()); got != 0 {
		t.Fatalf("expected back key 0, got %v", got)
	}

	// Touch key 0: move its slot to the front.
	i0, ok := r.Find(0)
	if !ok {
		t.Fatalf("key 0 should be present")
	}
	r.MoveToFront(i0)

	if got := r.Key(r.Back()); got != 1 {
		t.Fatalf("expected back key 1 after touching 0, got %v", got)
	}

	r.MoveToBack(i0)
	if got := r.Key(r.Back()); got != 0 {
		t.Fatalf("expected back key 0 after MoveToBack, got %v", got)
	}
}

func TestRing_remove(t *testing.T) {
	r := New[string, int](2)

	v := 5
	i := r.Back()
	r.Evict(i, "a", &v)
	if r.Len() != 1 || r.Back() == i {
		t.Fatalf("setup: len=%d, slot should be at front", r.Len())
	}

	r.Remove(i)

	if _, ok := r.Find("a"); ok {
		t.Fatalf("key should have been removed from index")
	}
	if r.Len() != 0 {
		t.Fatalf("bad len: %v", r.Len())
	}
	if got := r.Value(i); got != nil {
		t.Fatalf("value should be cleared, got %v", got)
	}
	if got := r.Key(i); got != "" {
		t.Fatalf("key should be zeroed so it does not outlive the entry, got %q", got)
	}
	if r.Back() != i {
		t.Fatalf("removed slot should be the next eviction victim")
	}
}

func TestRing_sizeOutOfRange(t *testing.T) {
	mustPanic := func(name string, size int) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s: New(%d) should panic", name, size)
			}
		}()
		New[int, int](size)
	}

	mustPanic("negative", -1)
	if math.MaxInt > MaxSize {
		mustPanic("too large", MaxSize+1)
	}
}

func TestRing_setValue(t *testing.T) {
	r := New[int, int](1)

	v := 1
	i := r.Back()
	r.Evict(i, 1, &v)

	v2 := 2
	r.SetValue(i, &v2)

	if got := r.Value(i); got == nil || *got != 2 {
		t.Fatalf("bad value after SetValue: %v", got)
	}
}
