package twoqueue

import (
	"math"
	"testing"
)

type nanStruct struct {
	id int
	f  float64
}

// assertNaNRefused checks the contract for a key that is not equal to
// itself: Set stores nothing and reports the pair as not held, lookups
// miss, and the queues and index are exactly as before.
func assertNaNRefused[K comparable](t *testing.T, l *TwoQueue[K, int], nan K, check func(K) bool) {
	t.Helper()

	before := struct{ len, index, ghost int }{l.Len(), len(l.index), l.recentEvict.Len()}

	for i := range 3 {
		e := l.Set(nan, 42+i)
		if e == nil || e.Value != 42+i || !check(e.Key) {
			t.Fatalf("Set(NaN) must return the pair just passed in, got %+v", e)
		}
	}

	if l.Contains(nan) || l.Get(nan) != nil || l.Peek(nan) != nil || l.Remove(nan) != nil {
		t.Fatal("a NaN key must be reported as absent")
	}
	if got := (struct{ len, index, ghost int }{l.Len(), len(l.index), l.recentEvict.Len()}); got != before {
		t.Fatalf("Set(NaN) changed the cache: before %+v, after %+v", before, got)
	}
	// Only Get counts, and it was called once.
	if s := l.Stats(); s.Hits != 0 || s.Misses != 1 {
		t.Fatalf("stats after one Get(NaN) = %+v, want 0 hits and 1 miss", s)
	}
}

func TestSet_NaNKeyIsRefused(t *testing.T) {
	nan := math.NaN()

	for _, split := range [][3]int{{2, 2, 2}, {0, 2, 2}, {2, 0, 2}, {2, 2, 0}, {0, 0, 0}} {
		l := NewParams[float64, int](split[0], split[1], split[2])
		l.Set(1, 1)
		l.Set(2, 2)
		assertNaNRefused(t, l, nan, math.IsNaN)
	}
}

func TestSet_NaNInsideCompositeKeys(t *testing.T) {
	nan := math.NaN()

	ls := NewParams[nanStruct, int](2, 2, 2)
	assertNaNRefused(t, ls, nanStruct{1, nan}, func(k nanStruct) bool { return k.id == 1 && math.IsNaN(k.f) })

	la := NewParams[[2]float64, int](2, 2, 2)
	assertNaNRefused(t, la, [2]float64{0, nan}, func(k [2]float64) bool { return math.IsNaN(k[1]) })

	li := NewParams[any, int](2, 2, 2)
	assertNaNRefused(t, li, any(nan), func(k any) bool { f, ok := k.(float64); return ok && math.IsNaN(f) })

	lc := NewParams[complex128, int](2, 2, 2)
	assertNaNRefused(t, lc, complex(nan, 0), func(k complex128) bool { return math.IsNaN(real(k)) })
}

// Ordinary float keys, including infinities and negative zero, still work.
func TestSet_NonNaNFloatKeysStillStored(t *testing.T) {
	l := NewParams[float64, int](4, 4, 4)
	for _, k := range []float64{0, math.Inf(1), math.Inf(-1), 1.5} {
		if e := l.Set(k, 1); e != nil {
			t.Fatalf("Set(%v): got %+v, want nil", k, e)
		}
	}
	if l.Len() != 4 {
		t.Fatalf("Len = %d, want 4", l.Len())
	}
	// -0 == +0, so it is the same key.
	if e := l.Set(math.Copysign(0, -1), 2); e == nil || e.Value != 1 {
		t.Fatalf("-0 must update the entry of +0, got %+v", e)
	}
}
