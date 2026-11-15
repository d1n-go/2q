package twoqueue

import "testing"

func TestNewParams_NegativeCapacityIsZero(t *testing.T) {
	l := NewParams[int, int](-1, -5, -100)

	if e := l.Set(1, 1); e == nil || e.Key != 1 {
		t.Fatalf("zero-capacity cache must report every Set as an immediate eviction, got %+v", e)
	}
	if v := l.Get(1); v != nil {
		t.Fatalf("zero-capacity cache must not store anything, got %d", *v)
	}
	if n := l.Len(); n != 0 {
		t.Fatalf("Len=%d, want 0", n)
	}
}

func TestNewParams_CapacityAboveMaxSizePanics(t *testing.T) {
	// Computed at run time: on 32-bit platforms the constant MaxSize+1
	// does not fit in int and would not even compile.
	tooLarge := MaxSize
	tooLarge++
	if tooLarge < 0 {
		t.Skip("int cannot exceed MaxSize on this platform")
	}
	for name, f := range map[string]func(){
		"Kin":  func() { NewParams[int, int](tooLarge, 0, 0) },
		"Kout": func() { NewParams[int, int](0, tooLarge, 0) },
		"size": func() { NewParams[int, int](0, 0, tooLarge) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s above MaxSize should panic", name)
				}
			}()
			f()
		}()
	}
}

// TestNewParams_ZeroFrequentDropsPromotedKeys pins the documented
// behaviour for size == 0: a key that would be promoted out of the ghost
// queue has nowhere to go and is dropped.
func TestNewParams_ZeroFrequentDropsPromotedKeys(t *testing.T) {
	l := NewParams[int, int](2, 2, 0)
	l.Set(1, 1)
	l.Set(2, 2)
	if e := l.Set(3, 3); e == nil || e.Key != 1 {
		t.Fatalf("1 should be evicted from recent into the ghost queue, got %+v", e)
	}

	if e := l.Set(1, 10); e == nil || e.Key != 1 || e.Value != 10 {
		t.Fatalf("promoting 1 with size=0 must report it as immediately evicted, got %+v", e)
	}
	if v := l.Get(1); v != nil {
		t.Fatalf("key 1 must not be stored anywhere, got %d", *v)
	}
	if n := l.Len(); n != 2 {
		t.Fatalf("Len=%d, want 2 (keys 2 and 3 in recent)", n)
	}
}

// TestNew_SmallSizes pins the documented consequences of New's truncated
// 25%/50%/75% splits for sizes below 4, where Kin ends up at zero.
func TestNew_SmallSizes(t *testing.T) {
	// New(1): every queue is empty, nothing is ever stored.
	l1 := New[int, int](1)
	l1.Set(1, 1)
	l1.Set(1, 1)
	if v := l1.Get(1); v != nil || l1.Len() != 0 {
		t.Fatalf("New(1) must never store anything, got %v len=%d", v, l1.Len())
	}

	// New(2), New(3): Kin=0, so the first Set of a key is reported as
	// evicted; the second Set promotes it from the ghost queue.
	for _, n := range []int{2, 3} {
		l := New[int, int](n)
		if e := l.Set(1, 1); e == nil || e.Key != 1 {
			t.Fatalf("New(%d): first Set must be reported as evicted, got %+v", n, e)
		}
		if e := l.Set(1, 1); e != nil {
			t.Fatalf("New(%d): second Set must promote silently, got %+v", n, e)
		}
		if v := l.Get(1); v == nil || *v != 1 {
			t.Fatalf("New(%d): key must be cached after the second Set, got %v", n, v)
		}
	}

	// New(4) is the first size where a single Set stores the key.
	l4 := New[int, int](4)
	if e := l4.Set(1, 1); e != nil {
		t.Fatalf("New(4): first Set must be stored, got %+v", e)
	}
	if v := l4.Get(1); v == nil || *v != 1 {
		t.Fatalf("New(4): key must be cached after one Set, got %v", v)
	}
}

// TestNew_Splits pins the capacities New derives from size: 25% for A1in,
// 50% for A1out and 75% for Am, each rounded down. The expected values are
// written out rather than computed, so a changed ratio cannot hide behind
// the same formula.
func TestNew_Splits(t *testing.T) {
	for _, tc := range []struct{ size, kin, kout, am int }{
		{-8, 0, 0, 0},
		{0, 0, 0, 0},
		{1, 0, 0, 0},
		{2, 0, 1, 1},
		{3, 0, 1, 2},
		{4, 1, 2, 3},
		{5, 1, 2, 3},
		{6, 1, 3, 4},
		{7, 1, 3, 5},
		{8, 2, 4, 6},
		{10, 2, 5, 7},
		{100, 25, 50, 75},
		{101, 25, 50, 75},
		{1023, 255, 511, 767},
		{8192, 2048, 4096, 6144},
	} {
		l := New[int, int](tc.size)
		if kin, kout, am := l.recent.Cap(), l.recentEvict.Cap(), l.frequent.Cap(); kin != tc.kin || kout != tc.kout || am != tc.am {
			t.Errorf("New(%d): Kin/Kout/Am = %d/%d/%d, want %d/%d/%d", tc.size, kin, kout, am, tc.kin, tc.kout, tc.am)
		}
	}
}
