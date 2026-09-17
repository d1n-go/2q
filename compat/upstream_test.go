// Package compat runs the fork side by side with the archived upstream
// modules it replaced (floatdrop/2q v0.1.1 on floatdrop/lru v1.2.1 and
// floatdrop/fifo v0.1.1) and pins down exactly how the two differ.
//
// The fork is meant to be a drop-in replacement, with one intentional
// behavioral change: upstream's lru/fifo evict a slot by unconditionally
// deleting the index entry for whatever key the slot last held, even when
// the slot is empty (never used, or freed by Remove). If that stale key is
// live in another slot, the live entry silently vanishes. internal/ring
// only deletes the index entry when the slot actually holds a value.
//
// That bug is reachable from a plain Set-only workload: promoting a key
// out of the ghost queue (A1out) Removes it there, leaving a stale-keyed
// empty slot, and the key can be evicted into a different ghost slot
// before the stale one is reused. Upstream then forgets the key was
// recently evicted and the next Set of it goes to recent instead of
// frequent, so eviction decisions diverge even without any user Remove.
// A direct "fork == upstream" comparison is therefore not the right
// statement. Instead the tests below run each implementation in lockstep
// with a slot-level model of the shared storage whose only knob is that
// unconditional delete: the fork must match the model with it switched
// off, upstream must match the model with it switched on. Together that
// establishes the unconditional delete as the only behavioral difference.
// Three deterministic tests then document the concrete consequences.
package compat

import (
	"fmt"
	"math/rand"
	"testing"

	fork "github.com/d1n-go/2q"
	upstream "github.com/floatdrop/2q"
)

// ---------------------------------------------------------------------
// A slot-level model of the lru/fifo storage shared by both
// implementations: a fixed number of preallocated slots kept in an
// MRU-to-victim order plus a key index. It is built from plain slices and
// shares no code with either side. The unconditionalDelete switch is the
// one and only place the two implementations are modelled differently.
// ---------------------------------------------------------------------

type slot struct {
	key int
	val *int
}

type slotQueue struct {
	slots               []slot
	order               []int // slot indices, front (MRU / newest) first, victim last
	index               map[int]int
	unconditionalDelete bool
}

func newSlotQueue(size int, unconditionalDelete bool) *slotQueue {
	q := &slotQueue{
		slots:               make([]slot, size),
		order:               make([]int, size),
		index:               make(map[int]int, size),
		unconditionalDelete: unconditionalDelete,
	}
	for i := range q.order {
		q.order[i] = i
	}
	return q
}

func (q *slotQueue) find(key int) (int, bool) {
	i, ok := q.index[key]
	return i, ok
}

func (q *slotQueue) position(i int) int {
	for p, s := range q.order {
		if s == i {
			return p
		}
	}
	panic("slot not in order")
}

func (q *slotQueue) moveToFront(i int) {
	p := q.position(i)
	q.order = append(q.order[:p], q.order[p+1:]...)
	q.order = append([]int{i}, q.order...)
}

func (q *slotQueue) moveToBack(i int) {
	p := q.position(i)
	q.order = append(q.order[:p], q.order[p+1:]...)
	q.order = append(q.order, i)
}

func (q *slotQueue) back() int { return q.order[len(q.order)-1] }

// evict reuses slot i for key/val and reports what the slot held before.
func (q *slotQueue) evict(i int, key int, val *int) (int, *int) {
	old := q.slots[i]
	if q.unconditionalDelete || old.val != nil {
		delete(q.index, old.key)
	}
	q.slots[i] = slot{key, val}
	q.index[key] = i
	q.moveToFront(i)
	return old.key, old.val
}

func (q *slotQueue) remove(key int) *int {
	i, ok := q.find(key)
	if !ok {
		return nil
	}
	v := q.slots[i].val
	q.moveToBack(i)
	q.slots[i].val = nil
	delete(q.index, key)
	return v
}

type modelLRU struct {
	q    *slotQueue
	size int
}

func (l *modelLRU) get(key int) *int {
	if i, ok := l.q.find(key); ok {
		l.q.moveToFront(i)
		return l.q.slots[i].val
	}
	return nil
}

func (l *modelLRU) peek(key int) *int {
	if i, ok := l.q.find(key); ok {
		return l.q.slots[i].val
	}
	return nil
}

func (l *modelLRU) set(key, val int) (evK, evV int, evicted bool) {
	if l.size < 1 {
		return key, val, true
	}
	if i, ok := l.q.find(key); ok {
		prev := *l.q.slots[i].val
		l.q.moveToFront(i)
		l.q.slots[i].val = &val
		return key, prev, true
	}
	ek, ev := l.q.evict(l.q.back(), key, &val)
	if ev != nil {
		return ek, *ev, true
	}
	return 0, 0, false
}

type modelFIFO struct {
	q    *slotQueue
	size int
}

func (f *modelFIFO) get(key int) *int {
	if i, ok := f.q.find(key); ok {
		return f.q.slots[i].val
	}
	return nil
}

func (f *modelFIFO) push(key, val int) (evK, evV int, evicted bool) {
	if f.size < 1 {
		return key, val, true
	}
	if i, ok := f.q.find(key); ok {
		f.q.slots[i].val = &val
		return 0, 0, false
	}
	ek, ev := f.q.evict(f.q.back(), key, &val)
	if ev != nil {
		return ek, *ev, true
	}
	return 0, 0, false
}

type model2Q struct {
	recent, recentEvict *modelFIFO
	frequent            *modelLRU
}

func newModel2Q(kin, kout, size int, unconditionalDelete bool) *model2Q {
	return &model2Q{
		recent:      &modelFIFO{newSlotQueue(kin, unconditionalDelete), kin},
		recentEvict: &modelFIFO{newSlotQueue(kout, unconditionalDelete), kout},
		frequent:    &modelLRU{newSlotQueue(size, unconditionalDelete), size},
	}
}

func (m *model2Q) get(key int) *int {
	if v := m.frequent.get(key); v != nil {
		return v
	}
	return m.recent.get(key)
}

func (m *model2Q) peek(key int) *int {
	if v := m.frequent.peek(key); v != nil {
		return v
	}
	return m.recent.get(key)
}

func (m *model2Q) remove(key int) *int {
	if v := m.frequent.q.remove(key); v != nil {
		return v
	}
	return m.recent.q.remove(key)
}

func (m *model2Q) set(key, val int) (evK, evV int, evicted bool) {
	if m.frequent.peek(key) != nil {
		return m.frequent.set(key, val)
	}
	if m.recentEvict.get(key) != nil {
		m.recentEvict.q.remove(key)
		return m.frequent.set(key, val)
	}
	if ek, ev, evicted := m.recent.push(key, val); evicted {
		ghost := 0
		m.recentEvict.push(ek, ghost)
		return ek, ev, true
	}
	return 0, 0, false
}

func (m *model2Q) length() int { return len(m.recent.q.index) + len(m.frequent.q.index) }

// ---------------------------------------------------------------------
// A uniform view over the fork, upstream and the model so the driver can
// run all of them in lockstep and compare observable results.
// ---------------------------------------------------------------------

type outcome struct {
	found      bool
	value      int
	evicted    bool
	evK, evV   int
	length     int
	extra      string // implementation-defined detail for diagnostics
	skipLength bool
}

func (o outcome) String() string {
	return fmt.Sprintf("found=%v value=%d evicted=%v ev=(%d,%d) len=%d", o.found, o.value, o.evicted, o.evK, o.evV, o.length)
}

type cache interface {
	get(key int) outcome
	set(key, val int) outcome
	peek(key int) outcome
	remove(key int) outcome
	length() int
}

func fromPtr(v *int) outcome {
	if v == nil {
		return outcome{}
	}
	return outcome{found: true, value: *v}
}

type forkCache struct{ c *fork.TwoQueue[int, int] }

func (f forkCache) get(k int) outcome    { return fromPtr(f.c.Get(k)) }
func (f forkCache) peek(k int) outcome   { return fromPtr(f.c.Peek(k)) }
func (f forkCache) remove(k int) outcome { return fromPtr(f.c.Remove(k)) }
func (f forkCache) length() int          { return f.c.Len() }
func (f forkCache) set(k, v int) outcome {
	if e := f.c.Set(k, v); e != nil {
		return outcome{evicted: true, evK: e.Key, evV: e.Value}
	}
	return outcome{}
}

type upstreamCache struct{ c *upstream.TwoQueue[int, int] }

func (u upstreamCache) get(k int) outcome    { return fromPtr(u.c.Get(k)) }
func (u upstreamCache) peek(k int) outcome   { return fromPtr(u.c.Peek(k)) }
func (u upstreamCache) remove(k int) outcome { return fromPtr(u.c.Remove(k)) }
func (u upstreamCache) length() int          { return u.c.Len() }
func (u upstreamCache) set(k, v int) outcome {
	if e := u.c.Set(k, v); e != nil {
		return outcome{evicted: true, evK: e.Key, evV: e.Value}
	}
	return outcome{}
}

type modelCache struct{ m *model2Q }

func (m modelCache) get(k int) outcome    { return fromPtr(m.m.get(k)) }
func (m modelCache) peek(k int) outcome   { return fromPtr(m.m.peek(k)) }
func (m modelCache) remove(k int) outcome { return fromPtr(m.m.remove(k)) }
func (m modelCache) length() int          { return m.m.length() }
func (m modelCache) set(k, v int) outcome {
	ek, ev, evicted := m.m.set(k, v)
	return outcome{evicted: evicted, evK: ek, evV: ev}
}

type opcode byte

const (
	opGet opcode = iota
	opSet
	opPeek
	opRemove
	numOps
)

func (o opcode) String() string { return [...]string{"Get", "Set", "Peek", "Remove"}[o] }

func apply(c cache, op opcode, key, val int) outcome {
	var o outcome
	switch op {
	case opGet:
		o = c.get(key)
	case opSet:
		o = c.set(key, val)
	case opPeek:
		o = c.peek(key)
	case opRemove:
		o = c.remove(key)
	}
	o.length = c.length()
	return o
}

// pair is one side-by-side comparison the driver keeps in lockstep.
type pair struct {
	name string
	a, b cache
}

// run replays ops as (opcode, key) byte pairs on every pair and fails on
// the first observable divergence within any pair.
func run(t *testing.T, kin, kout, size int, ops []byte, pairs []pair) {
	t.Helper()

	const keySpan = 6
	for i := 0; i+1 < len(ops); i += 2 {
		op := opcode(ops[i] % byte(numOps))
		key := int(ops[i+1]) % keySpan
		val := key*1000 + int(ops[i])

		for _, p := range pairs {
			oa := apply(p.a, op, key, val)
			ob := apply(p.b, op, key, val)
			if oa != ob {
				t.Fatalf("kin=%d kout=%d size=%d op#%d %s(%d): %s diverged\n  %s\n  %s",
					kin, kout, size, i/2, op, key, p.name, oa, ob)
			}
		}
	}
}

func newForkCache(kin, kout, size int) cache {
	return forkCache{fork.NewParams[int, int](kin, kout, size)}
}
func newUpstreamCache(kin, kout, size int) cache {
	return upstreamCache{upstream.NewParams[int, int](kin, kout, size)}
}

// modelPairs compares each implementation against the slot model with the
// unconditional-delete switch set the way that implementation behaves.
func modelPairs(kin, kout, size int) []pair {
	return []pair{
		{"fork vs model(unconditionalDelete=false)", newForkCache(kin, kout, size), modelCache{newModel2Q(kin, kout, size, false)}},
		{"upstream vs model(unconditionalDelete=true)", newUpstreamCache(kin, kout, size), modelCache{newModel2Q(kin, kout, size, true)}},
	}
}

func clampOps(ops []byte) []byte {
	if len(ops) > 4000 {
		return ops[:4000]
	}
	return ops
}

// FuzzForkAndUpstreamAgainstSlotModel exercises the full API (Remove and
// the zero key included) and pins each implementation to the slot model
// configured for its eviction semantics; see the package comment.
func FuzzForkAndUpstreamAgainstSlotModel(f *testing.F) {
	f.Add(2, 2, 2, []byte{1, 0, 1, 1, 3, 0, 1, 2, 0, 0})
	f.Add(3, 0, 0, []byte{1, 1, 1, 2, 1, 3, 3, 1, 3, 2, 1, 1, 1, 4, 0, 1})
	f.Add(4, 4, 4, []byte{1, 0, 1, 1, 1, 2, 1, 3, 1, 4, 1, 0, 2, 0, 3, 1, 1, 5})

	f.Fuzz(func(t *testing.T, kinByte, koutByte, sizeByte int, ops []byte) {
		kin, kout, size := int(uint(kinByte)%5), int(uint(koutByte)%5), int(uint(sizeByte)%5)
		run(t, kin, kout, size, clampOps(ops), modelPairs(kin, kout, size))
	})
}

func TestForkAndUpstreamAgainstSlotModelProperty(t *testing.T) {
	type cfg struct{ kin, kout, size int }
	for _, c := range []cfg{
		{0, 0, 0}, {1, 0, 0}, {0, 1, 1}, {1, 1, 1},
		{2, 2, 2}, {4, 4, 4}, {2, 4, 8}, {8, 2, 4}, {64, 128, 192},
	} {
		c := c
		r := rand.New(rand.NewSource(int64(c.kin*97 + c.kout*13 + c.size + 1)))
		ops := make([]byte, 8000)
		r.Read(ops)

		t.Run(fmt.Sprintf("kin=%d/kout=%d/size=%d", c.kin, c.kout, c.size), func(t *testing.T) {
			run(t, c.kin, c.kout, c.size, ops, modelPairs(c.kin, c.kout, c.size))
		})
	}
}

// TestKnownDivergence_ZeroKey documents the divergence for a never-used
// slot: its key field is int's zero value, so evicting it makes upstream
// drop the index entry of a real key 0 stored elsewhere.
func TestKnownDivergence_ZeroKey(t *testing.T) {
	u := upstream.NewParams[int, int](2, 2, 2)
	fk := fork.NewParams[int, int](2, 2, 2)

	for _, c := range []cache{upstreamCache{u}, forkCache{fk}} {
		c.set(0, 100) // key 0 lands in a fresh slot
		c.set(1, 101) // lands in the other, never-used slot whose stale key is 0
	}

	if v := u.Get(0); v != nil {
		t.Fatalf("upstream is expected to have lost key 0, but Get returned %d", *v)
	}
	if v := fk.Get(0); v == nil || *v != 100 {
		t.Fatalf("fork must keep key 0 = 100, got %v", v)
	}
	if ul, fl := u.Len(), fk.Len(); ul != 1 || fl != 2 {
		t.Fatalf("Len: upstream=%d (want 1, entry lost) fork=%d (want 2)", ul, fl)
	}
}

// TestKnownDivergence_RemovedKeyReinserted documents the same bug via
// Remove, which is reachable for any key, not just the zero value: Remove
// leaves the key in its (now empty) slot; if the key is Set again while
// that slot is still waiting to be reused, the eventual reuse deletes the
// live re-inserted entry from upstream's index.
func TestKnownDivergence_RemovedKeyReinserted(t *testing.T) {
	u := upstream.NewParams[int, int](3, 0, 0)
	fk := fork.NewParams[int, int](3, 0, 0)

	for _, c := range []cache{upstreamCache{u}, forkCache{fk}} {
		c.set(1, 1)
		c.set(2, 2)
		c.set(3, 3)  // recent is full: [3 2 1]
		c.remove(1)  // slot of 1 emptied, moved to back, still keyed 1
		c.remove(2)  // slot of 2 emptied, moved to back: [3 s1 s2]
		c.set(1, 10) // reuses s2 (the victim) for key 1: [s2=1 3 s1(stale key 1)]
		c.set(4, 4)  // reuses s1: upstream deletes index[1] and loses the live key 1
	}

	if v := u.Get(1); v != nil {
		t.Fatalf("upstream is expected to have lost key 1, but Get returned %d", *v)
	}
	if v := fk.Get(1); v == nil || *v != 10 {
		t.Fatalf("fork must keep key 1 = 10, got %v", v)
	}
	if ul, fl := u.Len(), fk.Len(); ul != 2 || fl != 3 {
		t.Fatalf("Len: upstream=%d (want 2, entry lost) fork=%d (want 3)", ul, fl)
	}
}

// TestKnownDivergence_GhostQueueForgetsKey shows the bug firing from
// nothing but Set calls on non-zero keys. Kin=2, Kout=2, size=2; ghost
// slots are g0/g1. Promoting 1 (op 5) and then 5 (op 6) Removes both
// from the ghost queue, leaving g1 stale-keyed 1 behind g0. Every later
// push lands in g0 because each promotion sends g0 straight back to the
// victim position, so g1 keeps its stale key 1 all along. Op 13 evicts
// key 1 from recent into g0; op 14 finally reuses g1 and upstream deletes
// index[1] - the live entry in g0. Op 15's Set(1) is then a ghost hit for
// the fork (promoted into frequent, evicting 6) but a miss for upstream
// (pushed into recent, evicting 3).
func TestKnownDivergence_GhostQueueForgetsKey(t *testing.T) {
	u := upstream.NewParams[int, int](2, 2, 2)
	fk := fork.NewParams[int, int](2, 2, 2)

	seq := []int{1, 5, 3, 6, 1, 5, 2, 3, 1, 6, 5, 2, 3, 4}
	for _, c := range []cache{upstreamCache{u}, forkCache{fk}} {
		for _, k := range seq {
			c.set(k, k)
		}
	}

	if e := u.Set(1, 1); e == nil || e.Key != 3 {
		t.Fatalf("upstream is expected to have forgotten ghost key 1 and evict 3 from recent, got %+v", e)
	}
	if e := fk.Set(1, 1); e == nil || e.Key != 6 {
		t.Fatalf("fork must promote key 1 into frequent and evict 6, got %+v", e)
	}
}
