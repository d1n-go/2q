package twoqueue

import (
	"testing"

	"github.com/d1n-go/2q/internal/ring"
)

// checkIndex asserts that the shared key index and the three queues agree
// exactly: every occupied slot is indexed at its own queue and slot, the
// index has no other entries, and each queue's Len matches its occupied
// slots. Since a map holds a key once, this also proves no key lives in
// two queues at once. It inspects internals, so l must be quiescent.
func checkIndex[K comparable, V any](t *testing.T, l *TwoQueue[K, V]) {
	t.Helper()

	occupied := 0
	occupied += checkQueue(t, l, inRecent, l.recent)
	occupied += checkQueue(t, l, inRecentEvict, l.recentEvict)
	occupied += checkQueue(t, l, inFrequent, l.frequent)
	if len(l.index) != occupied {
		t.Fatalf("index has %d entries but %d slots are occupied", len(l.index), occupied)
	}
}

func checkQueue[K comparable, V, W any](t *testing.T, l *TwoQueue[K, V], q queue, r *ring.Ring[K, W]) int {
	t.Helper()

	occupied := 0
	for slot := int32(0); int(slot) < r.Cap(); slot++ {
		if r.Value(slot) == nil {
			continue
		}
		occupied++
		key := r.Key(slot)
		if loc, ok := l.index[key]; !ok || loc != (location{q, slot}) {
			t.Fatalf("queue %d slot %d holds key %v, but the index has %+v (found=%v)", q, slot, key, loc, ok)
		}
	}
	if r.Len() != occupied {
		t.Fatalf("queue %d: Len()=%d but %d slots are occupied", q, r.Len(), occupied)
	}
	return occupied
}

// queueOf reports which queue holds key, for tests that pin placement.
func queueOf[K comparable, V any](l *TwoQueue[K, V], key K) (queue, bool) {
	loc, ok := l.index[key]
	return loc.queue, ok
}
