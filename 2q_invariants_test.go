package twoqueue

import (
	"fmt"
	"testing"

	"github.com/d1n-go/2q/internal/ring"
)

// checkIndex fails t if indexError finds a problem.
func checkIndex[K comparable, V any](t *testing.T, l *TwoQueue[K, V]) {
	t.Helper()

	if err := indexError(l); err != nil {
		t.Fatal(err)
	}
}

// indexError reports whether the shared key index and the three queues
// agree exactly: every occupied slot is indexed at its own queue and slot,
// the index has no other entries, and each queue's Len matches its
// occupied slots. Since a map holds a key once, this also proves no key
// lives in two queues at once. It inspects internals, so l must be
// quiescent.
func indexError[K comparable, V any](l *TwoQueue[K, V]) error {
	inRec, err := queueError(l, inRecent, l.recent)
	if err != nil {
		return err
	}
	inGhost, err := queueError(l, inRecentEvict, l.recentEvict)
	if err != nil {
		return err
	}
	inFreq, err := queueError(l, inFrequent, l.frequent)
	if err != nil {
		return err
	}
	if occupied := inRec + inGhost + inFreq; len(l.index) != occupied {
		return fmt.Errorf("index has %d entries but %d slots are occupied", len(l.index), occupied)
	}
	return nil
}

func queueError[K comparable, V, W any](l *TwoQueue[K, V], q queue, r *ring.Ring[K, W]) (int, error) {
	occupied := 0
	for slot := int32(0); int(slot) < r.Cap(); slot++ {
		if r.Value(slot) == nil {
			continue
		}
		occupied++
		key := r.Key(slot)
		if loc, ok := l.index[key]; !ok || loc != (location{q, slot}) {
			return 0, fmt.Errorf("queue %d slot %d holds key %v, but the index has %+v (found=%v)", q, slot, key, loc, ok)
		}
	}
	if r.Len() != occupied {
		return 0, fmt.Errorf("queue %d: Len()=%d but %d slots are occupied", q, r.Len(), occupied)
	}
	return occupied, nil
}

// queueOf reports which queue holds key, for tests that pin placement.
func queueOf[K comparable, V any](l *TwoQueue[K, V], key K) (queue, bool) {
	loc, ok := l.index[key]
	return loc.queue, ok
}
