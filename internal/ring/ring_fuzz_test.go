package ring

import (
	"fmt"
	"math/rand"
	"testing"
)

// checkInvariants walks the ring's linked list and cross-checks it against
// the caller's index map. Any divergence here (an unreachable slot, a stale
// index entry, a Len() that disagrees with what's actually linked) is
// exactly the shape of bug a ring buffer can introduce that container/list
// could not: silently losing or aliasing an entry while everything still
// compiles and "looks" fine.
func checkInvariants(t *testing.T, r *Ring[int, int], index map[int]int32) {
	t.Helper()

	n := len(r.nodes)
	visited := make([]bool, n)
	cur := r.root
	count := 0
	seenEmpty := false
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
		// Back, Evict and AppendKeys rely on the empty slots forming the
		// tail of the list.
		if r.nodes[cur].value == nil {
			seenEmpty = true
		} else if seenEmpty {
			t.Fatalf("occupied slot %d follows an empty one: empty slots must all sit at the back", cur)
		}
	}
	if count != n {
		t.Fatalf("traversal reached %d nodes, want all %d -> some slot is orphaned and its entry is unreachable", count, n)
	}

	live := 0
	for i := range r.root {
		node := r.nodes[i]
		if node.value == nil {
			continue
		}
		live++
		idx, ok := index[node.key]
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
	if len(index) != live {
		t.Fatalf("index has %d entries but only %d slots are occupied -> stale index entry aliasing a slot", len(index), live)
	}
}

// ringLRU drives Ring the way TwoQueue drives its Am queue: the index is
// kept by the caller and updated from what Evict and Remove report, so the
// fuzz driver below exercises the real usage contract rather than poking
// at Ring's primitives in isolation.
type ringLRU struct {
	r     *Ring[int, int]
	index map[int]int32
	size  int
}

func newRingLRU(size int) *ringLRU {
	return &ringLRU{r: New[int, int](size), index: map[int]int32{}, size: size}
}

func (l *ringLRU) get(key int) (int, bool) {
	if i, ok := l.index[key]; ok {
		l.r.MoveToFront(i)
		return *l.r.Value(i), true
	}
	return 0, false
}

func (l *ringLRU) peek(key int) (int, bool) {
	if i, ok := l.index[key]; ok {
		return *l.r.Value(i), true
	}
	return 0, false
}

func (l *ringLRU) set(key, val int) (evK, evV int, evicted bool) {
	if l.size < 1 {
		return key, val, true
	}
	if i, ok := l.index[key]; ok {
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
		delete(l.index, ek) // only for a slot that really held ek
	}
	l.index[key] = i
	if ev != nil {
		return ek, *ev, true
	}
	return 0, 0, false
}

func (l *ringLRU) remove(key int) (int, bool) {
	if i, ok := l.index[key]; ok {
		v := l.r.Value(i)
		l.r.Remove(i)
		delete(l.index, key)
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

func (l *ringLRU) purge() {
	l.r.Reset()
	clear(l.index)
}

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

func (m *lruModel) purge() { m.list = nil }

// opMix decodes op bytes 6-255 (0-5 map to the operations directly, so
// hand-written seeds stay readable). Taking every operation equally often
// would Remove as often as it Sets and Purge every sixth op, so the ring
// would rarely fill up and evict. Here Set dominates and there is no
// Purge; see decodeOp.
var opMix = [8]byte{1, 1, 1, 1, 0, 2, 3, 4}

// decodeOp maps an op byte to an operation (0 Get, 1 Set, 2 Peek, 3
// Remove, 4 Victim, 5 Purge). Purge needs op byte 5 and a key byte below
// 16 (1 in 4096 random ops); op byte 5 with a larger key byte is a Set.
func decodeOp(op, key byte) byte {
	switch {
	case op == 5 && key >= 16:
		return 1
	case op < 6:
		return op
	}
	return opMix[op%8]
}

// runSequence replays ops (pairs of opcode/key bytes) against a ring-backed
// LRU and the independent model in lockstep, failing on the first
// divergence, and re-validates the ring's internal invariants after every
// single operation.
func runSequence(t *testing.T, capacity int, ops []byte) {
	t.Helper()

	// Two more keys than slots: the ring can fill up and evict, yet keys
	// recur often enough to hit.
	keyDomain := max(6, capacity+2)
	rl := newRingLRU(capacity)
	model := &lruModel{cap: capacity}

	for i := 0; i+1 < len(ops); i += 2 {
		op := decodeOp(ops[i], ops[i+1])
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
		case 5:
			rl.purge()
			model.purge()
		}

		if rl.length() != model.length() {
			t.Fatalf("cap=%d Len mismatch after op %d on key %d: ring=%d model=%d", capacity, op, key, rl.length(), model.length())
		}
		checkInvariants(t, rl.r, rl.index)
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
	for _, capacity := range []int{0, 1, 2, 3, 5, 8, 16, 64, 200} {
		t.Run(fmt.Sprintf("cap=%d", capacity), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(capacity)*7919 + 1))
			ops := make([]byte, 100000)
			r.Read(ops)
			runSequence(t, capacity, ops)
		})
	}
}

// TestRing_neverUsedSlotDoesNotAliasRealEntry is a regression test for the
// upstream data-loss bug: evicting a never-used slot reports K's zero value
// as its key, and a caller that dropped that key from its index would lose
// a real entry stored under it elsewhere. Evict must report such a slot as
// empty (nil value) so the caller knows to leave its index alone.
func TestRing_neverUsedSlotDoesNotAliasRealEntry(t *testing.T) {
	l := newRingLRU(2)

	l.set(0, 100) // slot A gets the real, legitimate key 0
	// Slot B has never been used: its key field is still 0 (int's zero
	// value), value is nil.
	slot := l.r.Back()
	if ek, ev := l.r.Evict(slot, 1, new(int)); ev != nil {
		t.Fatalf("a never-used slot must be reported as empty, got key %d value %d", ek, *ev)
	}
	l.index[1] = slot

	if v, ok := l.peek(0); !ok || v != 100 {
		t.Fatalf("key 0 must still map to its real value 100, got %v (found=%v)", v, ok)
	}
	checkInvariants(t, l.r, l.index)
}
