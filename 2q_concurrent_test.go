package twoqueue

import (
	"math/rand"
	"runtime"
	"sync"
	"testing"
)

// checkNoDuplicateKeys asserts the core cross-queue invariant: a key lives
// in at most one queue (checkIndex), and Len() counts each cached key
// once. It is called with no other goroutines touching l, so the internal
// queues can be inspected directly.
func checkNoDuplicateKeys(t *testing.T, l *TwoQueue[int, int], keyDomain int) {
	t.Helper()

	checkIndex(t, l)
	present := 0
	for k := range keyDomain {
		if l.Contains(k) {
			present++
		}
	}
	if n := l.Len(); n != present {
		t.Fatalf("Len()=%d but %d distinct keys are actually cached", n, present)
	}
}

// TestTwoQueue_ConcurrentPromotionDoesNotDuplicateKey targets the specific
// interleaving that per-queue locking could not prevent: two goroutines Set
// the same key while it sits in the ghost queue. Without a cache-wide lock
// one of them promotes it into frequent while the other, having already
// seen "not in frequent" and "not in ghost", pushes a second copy into
// recent. Remove then deletes only one copy and Get resurrects the other.
func TestTwoQueue_ConcurrentPromotionDoesNotDuplicateKey(t *testing.T) {
	const rounds = 5000

	for round := range rounds {
		l := NewParams[int, int](1, 4, 4)
		l.Set(7, 1) // -> recent
		l.Set(8, 1) // recent is full: 7 moves to the ghost queue

		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				l.Set(7, 2)
			}()
		}
		wg.Wait()

		checkNoDuplicateKeys(t, l, 16)

		if v := l.Remove(7); v == nil {
			t.Fatalf("round %d: key 7 must be cached after the concurrent Set", round)
		}
		if v := l.Get(7); v != nil {
			t.Fatalf("round %d: key 7 resurrected after Remove: %d", round, *v)
		}
	}
}

const (
	stressKeyDomain   = 8
	stressOpsPerG     = 20000
	stressValueStride = 1 << 20 // > stressOpsPerG, so value/stressValueStride recovers the key
)

// stressOp applies the i-th operation of one goroutine's random stream to
// key and returns what Set returned, or nil for any other operation.
//
// Half of the operations are Sets and Purge is rare (1 in 4096). With
// Purge as likely as Set the cache is emptied as fast as it fills, so Am
// hardly ever holds enough keys to evict.
func stressOp(l *TwoQueue[int, int], r *rand.Rand, key, i int) *Evicted[int, int] {
	switch op := r.Intn(64); {
	case op < 32:
		return l.Set(key, key*stressValueStride+i)
	case op < 44:
		l.Get(key)
	case op < 50:
		l.Remove(key)
	case op < 53:
		l.Peek(key)
	case op < 56:
		l.Contains(key)
	case op == 56:
		_ = l.Len()
	case op == 57:
		_ = l.Keys()
	case op == 58:
		_ = l.Stats()
	case op == 59:
		_ = l.ResetStats()
	case op == 60 && r.Intn(64) == 0:
		l.Purge()
	default:
		l.Get(key)
	}
	return nil
}

// TestTwoQueue_ConcurrentStress hammers one small cache from many
// goroutines with a random mix of every public method over a tiny key
// space (so the same keys constantly collide and migrate between queues),
// then validates the cross-queue invariants once everything is quiescent.
// Run under -race this also covers the internal queues, which no longer
// carry locks of their own.
//
// Which transitions a run takes cannot be observed while it is concurrent,
// so each configuration first replays one goroutine's stream alone and
// requires that it reaches every one of them, eviction from a full Am
// included: a mix that never fills the queues would pass vacuously.
func TestTwoQueue_ConcurrentStress(t *testing.T) {
	goroutines := 4 * runtime.GOMAXPROCS(0)

	type cfg struct{ kin, kout, size int }
	for _, c := range []cfg{{1, 1, 1}, {2, 4, 2}, {2, 2, 4}, {0, 4, 4}} {
		l := NewParams[int, int](c.kin, c.kout, c.size)

		var paged, promoted, amEvicted int
		r := rand.New(rand.NewSource(1))
		for i := range stressOpsPerG {
			key := r.Intn(stressKeyDomain)
			from, known := queueOf(l, key)
			e := stressOp(l, r, key, i)
			switch evicted := e != nil && e.Key != key; {
			case !known && evicted:
				paged++ // a new key pushed the oldest one out of A1in
			case known && from == inRecentEvict && evicted:
				promoted++
				amEvicted++ // a promotion found Am full
			case known && from == inRecentEvict && e == nil:
				promoted++
			}
		}
		if (c.kin > 0 && paged == 0) || promoted == 0 || amEvicted == 0 {
			t.Fatalf("kin=%d kout=%d size=%d: the op mix does not exercise the cache: %d A1in evictions, %d promotions, %d Am evictions",
				c.kin, c.kout, c.size, paged, promoted, amEvicted)
		}
		checkNoDuplicateKeys(t, l, stressKeyDomain)

		var wg sync.WaitGroup
		for g := range goroutines {
			wg.Add(1)
			go func(seed int64) {
				defer wg.Done()
				r := rand.New(rand.NewSource(seed))
				for i := range stressOpsPerG {
					stressOp(l, r, r.Intn(stressKeyDomain), i)
				}
			}(int64(g + 2))
		}
		wg.Wait()

		checkNoDuplicateKeys(t, l, stressKeyDomain)

		// Every remaining entry must still be readable and carry a value
		// that some Set actually wrote for that key.
		for k := range stressKeyDomain {
			if v := l.Peek(k); v != nil && *v/stressValueStride != k {
				t.Fatalf("kin=%d kout=%d size=%d: key %d holds foreign value %d", c.kin, c.kout, c.size, k, *v)
			}
		}
	}
}
