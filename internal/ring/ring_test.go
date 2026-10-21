package ring

import (
	"math"
	"slices"
	"testing"
)

func TestRing_zero(t *testing.T) {
	r := New[int, int](0)

	if r.Len() != 0 || r.Cap() != 0 {
		t.Fatalf("bad len/cap: %v/%v", r.Len(), r.Cap())
	}

	if got := r.AppendKeys(nil); len(got) != 0 {
		t.Fatalf("empty ring should not contain any key: %v", got)
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

	if r.Key(i) != 1 || r.Value(i) != &v {
		t.Fatalf("slot %v should now hold key 1, got key %v value %v", i, r.Key(i), r.Value(i))
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

	if r.Key(i) != 2 || r.Value(i) != &v2 {
		t.Fatalf("slot %v should now hold key 2, got key %v value %v", i, r.Key(i), r.Value(i))
	}

	if r.Len() != 1 {
		t.Fatalf("replacing an occupied slot must not change len: %v", r.Len())
	}
}

func TestRing_moveToFrontAndBack(t *testing.T) {
	r := New[int, int](3)

	slots := make([]int32, 3)
	for k := range 3 {
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
	i0 := slots[0]
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

	if got := r.AppendKeys(nil); len(got) != 0 {
		t.Fatalf("removed key should not be listed: %v", got)
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
	// Computed at run time: on 32-bit platforms the constant MaxSize+1
	// does not fit in int and would not even compile.
	if tooLarge := MaxSize; tooLarge < math.MaxInt {
		tooLarge++
		mustPanic("too large", tooLarge)
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

func TestRing_nextPrev(t *testing.T) {
	r := New[int, int](3)
	for k := range 2 {
		v := k
		r.Evict(r.Back(), k, &v)
	}

	// Front to back: key 1, key 0, then the one empty slot.
	var keys []int
	for i := r.Next(int32(r.Cap())); int(i) != r.Cap(); i = r.Next(i) {
		if r.Prev(r.Next(i)) != i {
			t.Fatalf("slot %d: Prev(Next) = %d", i, r.Prev(r.Next(i)))
		}
		keys = append(keys, r.Key(i))
	}
	if want := []int{1, 0, 0}; !slices.Equal(keys, want) {
		t.Fatalf("walk from the front gave keys %v, want %v", keys, want)
	}
	if r.Prev(int32(r.Cap())) != r.Back() {
		t.Fatalf("Prev of the sentinel is %d, Back is %d", r.Prev(int32(r.Cap())), r.Back())
	}
}

func TestRing_clone(t *testing.T) {
	r := New[int, int](2)
	v := 1
	r.Evict(r.Back(), 1, &v)

	c := r.Clone()
	w := 2
	c.Evict(c.Back(), 2, &w)
	c.Remove(c.Back()) // the slot holding key 1

	if r.Len() != 1 || !slices.Equal(r.AppendKeys(nil), []int{1}) {
		t.Fatalf("changing the clone changed the original: len=%d keys=%v", r.Len(), r.AppendKeys(nil))
	}
	if c.Len() != 1 || !slices.Equal(c.AppendKeys(nil), []int{2}) {
		t.Fatalf("clone: len=%d keys=%v, want 1 [2]", c.Len(), c.AppendKeys(nil))
	}
}
