package ring

import (
	"fmt"
	"math/rand"
	"testing"
)

// checkInvariants walks the ring's linked list and cross-checks it against
// the index map. Any divergence here (an unreachable slot, a stale index
// entry, a Len() that disagrees with what's actually linked) is exactly the
// shape of bug a ring buffer can introduce that container/list+map could
// not: silently losing or aliasing an entry while everything still compiles
// and "looks" fine.
func checkInvariants(t *testing.T, r *Ring[int, int]) {
	t.Helper()

	n := len(r.nodes)
	visited := make([]bool, n)
	cur := r.root
	count := 0
	for {
		if visited[cur] {
			t.Fatalf("cycle revisits node %d before covering all %d nodes (visited %d) -> ring is broken into multiple loops", cur, n, count)
		}
		visited[cur] = true
		count++
		next := r.nodes[cur].next
		if r.nodes[next].prev != cur {
			t.Fatalf("broken link: node %d.next=%d but node %d.prev=%d", cur, next, next, r.nodes[next].prev)
		}
		cur = next
		if cur == r.root {
			break
		}
	}
	if count != n {
		t.Fatalf("traversal reached %d nodes, want all %d -> some slot is orphaned and its entry is unreachable", count, n)
	}

	live := 0
	for i := int32(0); i < r.root; i++ {
		node := r.nodes[i]
		if node.value == nil {
			continue
		}
		live++
		idx, ok := r.index[node.key]
		if !ok {
			t.Fatalf("slot %d holds key %v with a value but has no index entry -> DATA LOSS on next Find", i, node.key)
		}
		if idx != i {
			t.Fatalf("index for key %v points at slot %d, but the value actually lives in slot %d", node.key, idx, i)
		}
	}
	if live != r.Len() {
		t.Fatalf("Len()=%d disagrees with %d occupied slots found by traversal", r.Len(), live)
	}
	if len(r.index) != live {
		t.Fatalf("index has %d entries but only %d slots are occupied -> stale index entry aliasing a slot", len(r.index), live)
	}
}

// ringLRU drives Ring exactly the way internal/lru.LRU does (same call
// sequence, mutex dropped since this is single-goroutine), so the fuzz
// driver below exercises the real usage contract rather than poking at
// Ring's primitives in isolation.
type ringLRU struct {
	r    *Ring[int, int]
	size int
}

func newRingLRU(size int) *ringLRU { return &ringLRU{r: New[int, int](size), size: size} }

func (l *ringLRU) get(key int) (int, bool) {
	if i, ok := l.r.Find(key); ok {
		l.r.MoveToFront(i)
		return *l.r.Value(i), true
	}
	return 0, false
}

func (l *ringLRU) peek(key int) (int, bool) {
	if i, ok := l.r.Find(key); ok {
		return *l.r.Value(i), true
	}
	return 0, false
}

func (l *ringLRU) set(key, val int) (evK, evV int, evicted bool) {
	if l.size < 1 {
		return key, val, true
	}
	if i, ok := l.r.Find(key); ok {
		prev := *l.r.Value(i)
		l.r.MoveToFront(i)
		v := val
		l.r.SetValue(i, &v)
		return key, prev, true
	}
	i := l.r.Back()
	v := val
	ek, ev := l.r.Evict(i, key, &v)
	if ev != nil {
		return ek, *ev, true
	}
	return 0, 0, false
}

func (l *ringLRU) remove(key int) (int, bool) {
	if i, ok := l.r.Find(key); ok {
		v := l.r.Value(i)
		l.r.MoveToBack(i)
		l.r.SetValue(i, nil)
		l.r.DeleteIndex(key)
		return *v, true
	}
	return 0, false
}

func (l *ringLRU) victim() (int, bool) {
	if l.size < 1 {
		return 0, false
	}
	i := l.r.Back()
	if v := l.r.Value(i); v != nil {
		return l.r.Key(i), true
	}
	return 0, false
}

func (l *ringLRU) length() int { return l.r.Len() }

// lruModel is an independent reference LRU built on plain slice shifting
// (no fixed slots, no linked list, no reused storage). It shares the
// documented contract with ringLRU but not the implementation strategy, so
// it can catch ring/index bugs a self-consistency check alone might miss.
type lruModel struct {
	cap  int
	list []struct{ key, val int } // front = MRU
}

func (m *lruModel) find(key int) int {
	for i, e := range m.list {
		if e.key == key {
			return i
		}
	}
	return -1
}

func (m *lruModel) moveFront(i int) {
	e := m.list[i]
	m.list = append(m.list[:i], m.list[i+1:]...)
	m.list = append([]struct{ key, val int }{e}, m.list...)
}

func (m *lruModel) get(key int) (int, bool) {
	if i := m.find(key); i >= 0 {
		v := m.list[i].val
		m.moveFront(i)
		return v, true
	}
	return 0, false
}

func (m *lruModel) peek(key int) (int, bool) {
	if i := m.find(key); i >= 0 {
		return m.list[i].val, true
	}
	return 0, false
}

func (m *lruModel) set(key, val int) (evK, evV int, evicted bool) {
	if m.cap < 1 {
		return key, val, true
	}
	if i := m.find(key); i >= 0 {
		prev := m.list[i].val
		m.list[i].val = val
		m.moveFront(i)
		return key, prev, true
	}
	m.list = append([]struct{ key, val int }{{key, val}}, m.list...)
	if len(m.list) > m.cap {
		last := m.list[len(m.list)-1]
		m.list = m.list[:len(m.list)-1]
		return last.key, last.val, true
	}
	return 0, 0, false
}

func (m *lruModel) remove(key int) (int, bool) {
	if i := m.find(key); i >= 0 {
		v := m.list[i].val
		m.list = append(m.list[:i], m.list[i+1:]...)
		return v, true
	}
	return 0, false
}

func (m *lruModel) victim() (int, bool) {
	if m.cap < 1 || len(m.list) < m.cap {
		return 0, false
	}
	last := m.list[len(m.list)-1]
	return last.key, true
}

func (m *lruModel) length() int { return len(m.list) }

// runSequence replays ops (pairs of opcode/key bytes) against a ring-backed
// LRU and the independent model in lockstep, failing on the first
// divergence, and re-validates the ring's internal invariants after every
// single operation.
func runSequence(t *testing.T, capacity int, ops []byte) {
	t.Helper()

	const keyDomain = 6
	rl := newRingLRU(capacity)
	model := &lruModel{cap: capacity}

	for i := 0; i+1 < len(ops); i += 2 {
		op := ops[i] % 5
		key := int(ops[i+1]) % keyDomain
		val := key*1000 + int(ops[i])

		switch op {
		case 0:
			gv, gok := rl.get(key)
			mv, mok := model.get(key)
			if gok != mok || (gok && gv != mv) {
				t.Fatalf("cap=%d Get(%d) mismatch: ring=(%d,%v) model=(%d,%v)", capacity, key, gv, gok, mv, mok)
			}
		case 1:
			ek1, ev1, evd1 := rl.set(key, val)
			ek2, ev2, evd2 := model.set(key, val)
			if evd1 != evd2 || (evd1 && (ek1 != ek2 || ev1 != ev2)) {
				t.Fatalf("cap=%d Set(%d,%d) mismatch: ring evicted=(%d,%d,%v) model evicted=(%d,%d,%v)", capacity, key, val, ek1, ev1, evd1, ek2, ev2, evd2)
			}
		case 2:
			gv, gok := rl.peek(key)
			mv, mok := model.peek(key)
			if gok != mok || (gok && gv != mv) {
				t.Fatalf("cap=%d Peek(%d) mismatch: ring=(%d,%v) model=(%d,%v)", capacity, key, gv, gok, mv, mok)
			}
		case 3:
			gv, gok := rl.remove(key)
			mv, mok := model.remove(key)
			if gok != mok || (gok && gv != mv) {
				t.Fatalf("cap=%d Remove(%d) mismatch: ring=(%d,%v) model=(%d,%v)", capacity, key, gv, gok, mv, mok)
			}
		case 4:
			gv, gok := rl.victim()
			mv, mok := model.victim()
			if gok != mok || (gok && gv != mv) {
				t.Fatalf("cap=%d Victim mismatch: ring=(%d,%v) model=(%d,%v)", capacity, gv, gok, mv, mok)
			}
		}

		if rl.length() != model.length() {
			t.Fatalf("cap=%d Len mismatch after op %d on key %d: ring=%d model=%d", capacity, op, key, rl.length(), model.length())
		}
		checkInvariants(t, rl.r)
	}
}

func FuzzRingAgainstModel(f *testing.F) {
	f.Add(0, []byte{0, 1, 0, 2, 0, 3, 0, 4, 1, 1, 2, 2, 3, 3})
	f.Add(4, []byte{1, 0, 1, 1, 1, 2, 1, 3, 1, 4, 1, 5, 3, 0, 4, 0})
	f.Add(1, []byte{1, 0, 1, 0, 1, 1, 0, 0, 3, 0, 1, 0})

	f.Fuzz(func(t *testing.T, capByte int, ops []byte) {
		if len(ops) > 4000 {
			ops = ops[:4000]
		}
		capacity := int(uint(capByte) % 9)
		runSequence(t, capacity, ops)
	})
}

// TestRingProperty covers a spread of capacities (including the 0/1 edge
// cases) with long, deterministic pseudo-random sequences, independently of
// whatever the native fuzz corpus happens to contain.
func TestRingProperty(t *testing.T) {
	for _, capacity := range []int{0, 1, 2, 3, 5, 8, 16} {
		capacity := capacity
		t.Run(fmt.Sprintf("cap=%d", capacity), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(capacity)*7919 + 1))
			ops := make([]byte, 8000)
			r.Read(ops)
			runSequence(t, capacity, ops)
		})
	}
}

// TestRing_neverUsedSlotDoesNotAliasRealEntry is a regression test for the
// bug fixed by internal/ring (see its package doc / the vendoring commit):
// evicting a never-used slot must not delete the index entry for K's zero
// value if a *different* slot legitimately holds that key.
func TestRing_neverUsedSlotDoesNotAliasRealEntry(t *testing.T) {
	r := New[int, int](2)

	// Slot A gets the real, legitimate key 0.
	v0 := 100
	r.Evict(r.Back(), 0, &v0)

	// Slot B has never been used: its key field is still 0 (int's zero
	// value), value is nil. Evicting it must not touch key 0's index entry,
	// which belongs to slot A.
	v1 := 200
	r.Evict(r.Back(), 1, &v1)

	if i, ok := r.Find(0); !ok {
		t.Fatalf("key 0 must still be indexed after evicting an unrelated never-used slot")
	} else if v := r.Value(i); v == nil || *v != 100 {
		t.Fatalf("key 0 must still map to its real value 100, got %v", v)
	}
}
