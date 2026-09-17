package twoqueue

import (
	"math/rand"
	"runtime"
	"sync"
	"testing"
)

// checkNoDuplicateKeys asserts the core cross-queue invariant: a key lives
// in at most one of recent/frequent, and Len() counts each key once. It is
// called with no other goroutines touching l, so the internal queues can be
// inspected directly.
func checkNoDuplicateKeys(t *testing.T, l *TwoQueue[int, int], keyDomain int) {
	t.Helper()

	present := 0
	for k := 0; k < keyDomain; k++ {
		inRecent := l.recent.Get(k) != nil
		inFrequent := l.frequent.Peek(k) != nil
		if inRecent && inFrequent {
			t.Fatalf("key %d is present in both recent and frequent", k)
		}
		if inRecent || inFrequent {
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

	for round := 0; round < rounds; round++ {
		l := NewParams[int, int](1, 4, 4)
		l.Set(7, 1) // -> recent
		l.Set(8, 1) // recent is full: 7 moves to the ghost queue

		var wg sync.WaitGroup
		for g := 0; g < 2; g++ {
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

// TestTwoQueue_ConcurrentStress hammers one small cache from many
// goroutines with a random mix of every public method over a tiny key
// space (so the same keys constantly collide and migrate between queues),
// then validates the cross-queue invariants once everything is quiescent.
// Run under -race this also covers the internal queues, which no longer
// carry locks of their own.
func TestTwoQueue_ConcurrentStress(t *testing.T) {
	const (
		keyDomain   = 8
		opsPerG     = 20000
		valueStride = 1 << 20 // > opsPerG, so value/valueStride recovers the key
	)
	goroutines := 4 * runtime.GOMAXPROCS(0)

	type cfg struct{ kin, kout, size int }
	for _, c := range []cfg{{1, 1, 1}, {2, 4, 2}, {2, 2, 4}, {0, 4, 4}} {
		l := NewParams[int, int](c.kin, c.kout, c.size)

		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(seed int64) {
				defer wg.Done()
				r := rand.New(rand.NewSource(seed))
				for i := 0; i < opsPerG; i++ {
					key := r.Intn(keyDomain)
					switch r.Intn(64) {
					case 0:
						l.Get(key)
					case 1:
						l.Set(key, key*valueStride+i)
					case 2:
						l.Peek(key)
					case 3:
						l.Remove(key)
					case 4:
						_ = l.Len()
					case 5:
						l.Purge()
					default:
						l.Get(key)
					}
				}
			}(int64(g + 1))
		}
		wg.Wait()

		checkNoDuplicateKeys(t, l, keyDomain)

		// Every remaining entry must still be readable and carry a value
		// that some Set actually wrote for that key.
		for k := 0; k < keyDomain; k++ {
			if v := l.Peek(k); v != nil && *v/valueStride != k {
				t.Fatalf("kin=%d kout=%d size=%d: key %d holds foreign value %d", c.kin, c.kout, c.size, k, *v)
			}
		}
	}
}
