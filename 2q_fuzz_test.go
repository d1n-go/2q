package twoqueue

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"
)

// The types below are an independent re-implementation of the 2Q Full
// Version promotion algorithm (recent FIFO -> recentEvict ghost FIFO ->
// frequent LRU) built from plain slices instead of TwoQueue's shared
// index over ring slots. It is the ground truth for the differential fuzz
// test below: since it shares no code with the real implementation, any
// divergence is a genuine behavioral bug rather than a shared assumption
// baked into both sides.

type oracleEntry struct{ key, val int }

type oracleFIFO struct {
	cap  int
	list []oracleEntry // front = newest push, back = next victim
}

func (o *oracleFIFO) find(key int) int {
	for i, e := range o.list {
		if e.key == key {
			return i
		}
	}
	return -1
}

// get is a FIFO lookup: it never reorders the queue.
func (o *oracleFIFO) get(key int) (int, bool) {
	if i := o.find(key); i >= 0 {
		return o.list[i].val, true
	}
	return 0, false
}

func (o *oracleFIFO) push(key, val int) (evK, evV int, evicted bool) {
	if o.cap < 1 {
		return key, val, true
	}
	if i := o.find(key); i >= 0 {
		o.list[i].val = val
		return 0, 0, false
	}
	o.list = append([]oracleEntry{{key, val}}, o.list...)
	if len(o.list) > o.cap {
		last := o.list[len(o.list)-1]
		o.list = o.list[:len(o.list)-1]
		return last.key, last.val, true
	}
	return 0, 0, false
}

func (o *oracleFIFO) remove(key int) (int, bool) {
	if i := o.find(key); i >= 0 {
		v := o.list[i].val
		o.list = append(o.list[:i], o.list[i+1:]...)
		return v, true
	}
	return 0, false
}

func (o *oracleFIFO) length() int { return len(o.list) }

type oracleLRU struct {
	cap  int
	list []oracleEntry // front = MRU
}

func (o *oracleLRU) find(key int) int {
	for i, e := range o.list {
		if e.key == key {
			return i
		}
	}
	return -1
}

func (o *oracleLRU) moveFront(i int) {
	e := o.list[i]
	o.list = append(o.list[:i], o.list[i+1:]...)
	o.list = append([]oracleEntry{e}, o.list...)
}

func (o *oracleLRU) get(key int) (int, bool) {
	if i := o.find(key); i >= 0 {
		v := o.list[i].val
		o.moveFront(i)
		return v, true
	}
	return 0, false
}

func (o *oracleLRU) peek(key int) (int, bool) {
	if i := o.find(key); i >= 0 {
		return o.list[i].val, true
	}
	return 0, false
}

func (o *oracleLRU) set(key, val int) (evK, evV int, evicted bool) {
	if o.cap < 1 {
		return key, val, true
	}
	if i := o.find(key); i >= 0 {
		prev := o.list[i].val
		o.list[i].val = val
		o.moveFront(i)
		return key, prev, true
	}
	o.list = append([]oracleEntry{{key, val}}, o.list...)
	if len(o.list) > o.cap {
		last := o.list[len(o.list)-1]
		o.list = o.list[:len(o.list)-1]
		return last.key, last.val, true
	}
	return 0, 0, false
}

func (o *oracleLRU) remove(key int) (int, bool) {
	if i := o.find(key); i >= 0 {
		v := o.list[i].val
		o.list = append(o.list[:i], o.list[i+1:]...)
		return v, true
	}
	return 0, false
}

func (o *oracleLRU) length() int { return len(o.list) }

type oracle2Q struct {
	recent      oracleFIFO
	recentEvict oracleFIFO
	frequent    oracleLRU
}

func newOracle2Q(kin, kout, size int) *oracle2Q {
	return &oracle2Q{
		recent:      oracleFIFO{cap: kin},
		recentEvict: oracleFIFO{cap: kout},
		frequent:    oracleLRU{cap: size},
	}
}

func (o *oracle2Q) get(key int) (int, bool) {
	if v, ok := o.frequent.get(key); ok {
		return v, true
	}
	return o.recent.get(key)
}

func (o *oracle2Q) peek(key int) (int, bool) {
	if v, ok := o.frequent.peek(key); ok {
		return v, true
	}
	return o.recent.get(key) // a FIFO get never reorders, so it doubles as Peek
}

func (o *oracle2Q) remove(key int) (int, bool) {
	if v, ok := o.frequent.remove(key); ok {
		return v, true
	}
	return o.recent.remove(key)
}

func (o *oracle2Q) set(key, val int) (evK, evV int, evicted bool) {
	if _, ok := o.frequent.peek(key); ok {
		return o.frequent.set(key, val)
	}
	if prev, ok := o.recent.get(key); ok {
		o.recent.push(key, val) // in-place update, reported like an LRU update
		return key, prev, true
	}
	if _, ok := o.recentEvict.get(key); ok {
		o.recentEvict.remove(key)
		return o.frequent.set(key, val)
	}
	if evK, evV, evicted := o.recent.push(key, val); evicted {
		o.recentEvict.push(evK, 0)
		return evK, evV, true
	}
	return 0, 0, false
}

func (o *oracle2Q) length() int { return o.recent.length() + o.frequent.length() }

func (o *oracle2Q) contains(key int) bool {
	_, ok := o.frequent.peek(key)
	if !ok {
		_, ok = o.recent.get(key)
	}
	return ok
}

// keys mirrors TwoQueue.Keys: frequent MRU->LRU, then recent newest->oldest.
func (o *oracle2Q) keys() []int {
	var ks []int
	for _, e := range o.frequent.list {
		ks = append(ks, e.key)
	}
	for _, e := range o.recent.list {
		ks = append(ks, e.key)
	}
	return ks
}

func (o *oracle2Q) purge() {
	o.recent.list, o.recentEvict.list, o.frequent.list = nil, nil, nil
}

// The operations runTwoQueueSequence replays. Op bytes 0-7 decode to
// them directly, so hand-written seeds stay readable.
const (
	opGet byte = iota
	opSet
	opPeek
	opRemove
	opPurge
	opContains
	opKeys
	opResetStats
)

// opMix decodes op bytes 8-255. Taking every opcode equally often would
// Remove as often as it Sets and Purge every eighth op, draining the cache
// faster than it fills, so Am and A1out would hardly ever evict. Here Set
// dominates and there is no Purge; see decodeOp.
var opMix = [16]byte{
	opSet, opSet, opSet, opSet, opSet, opSet, opSet, opSet,
	opGet, opGet, opRemove, opRemove, opPeek, opContains, opKeys, opResetStats,
}

// decodeOp maps an op byte to an operation. Purge needs op byte 4 and a
// key byte below 16 (1 in 4096 random ops), so that even the largest
// configurations fill up between Purges; op byte 4 with a larger key byte
// is a Set.
func decodeOp(op, key byte) byte {
	switch {
	case op == opPurge && key >= 16:
		return opSet
	case op < 8:
		return op
	}
	return opMix[op%16]
}

// keyDomainFor returns how many distinct keys a sequence draws from: two
// more than the three queues can hold together, so each of them can fill
// up and evict, yet few enough that keys recur while still remembered.
// Keys are decoded from one byte, so callers keep it at most 256.
func keyDomainFor(kin, kout, size int) int {
	return max(6, kin+kout+size+2)
}

// runTwoQueueSequence replays ops (pairs of opcode/key bytes) against the
// real TwoQueue and the independent oracle in lockstep, failing on the
// first divergence in returned values, evicted entries, or Len().
func runTwoQueueSequence(t *testing.T, kin, kout, size int, ops []byte) {
	t.Helper()

	keyDomain := keyDomainFor(kin, kout, size)
	tq := NewParams[int, int](kin, kout, size)
	ora := newOracle2Q(kin, kout, size)
	var want Stats

	for i := 0; i+1 < len(ops); i += 2 {
		op := decodeOp(ops[i], ops[i+1])
		key := int(ops[i+1]) % keyDomain
		val := key*1000 + int(ops[i])

		switch op {
		case opGet:
			var gv int
			gp := tq.Get(key)
			if gp != nil {
				gv = *gp
			}
			mv, mok := ora.get(key)
			if (gp != nil) != mok || (mok && gv != mv) {
				t.Fatalf("kin=%d kout=%d size=%d Get(%d) mismatch: real=(%v,%v) oracle=(%v,%v)", kin, kout, size, key, gv, gp != nil, mv, mok)
			}
			if mok {
				want.Hits++
			} else {
				want.Misses++
			}
		case opSet:
			e := tq.Set(key, val)
			evK, evV, evicted := ora.set(key, val)
			if (e != nil) != evicted || (evicted && (e.Key != evK || e.Value != evV)) {
				t.Fatalf("kin=%d kout=%d size=%d Set(%d,%d) mismatch: real=%+v oracle=(%d,%d,%v)", kin, kout, size, key, val, e, evK, evV, evicted)
			}
		case opPeek:
			var gv int
			gp := tq.Peek(key)
			if gp != nil {
				gv = *gp
			}
			mv, mok := ora.peek(key)
			if (gp != nil) != mok || (mok && gv != mv) {
				t.Fatalf("kin=%d kout=%d size=%d Peek(%d) mismatch: real=(%v,%v) oracle=(%v,%v)", kin, kout, size, key, gv, gp != nil, mv, mok)
			}
		case opRemove:
			var gv int
			gp := tq.Remove(key)
			if gp != nil {
				gv = *gp
			}
			mv, mok := ora.remove(key)
			if (gp != nil) != mok || (mok && gv != mv) {
				t.Fatalf("kin=%d kout=%d size=%d Remove(%d) mismatch: real=(%v,%v) oracle=(%v,%v)", kin, kout, size, key, gv, gp != nil, mv, mok)
			}
		case opPurge:
			tq.Purge()
			ora.purge()
		case opContains:
			if got, want := tq.Contains(key), ora.contains(key); got != want {
				t.Fatalf("kin=%d kout=%d size=%d Contains(%d) mismatch: real=%v oracle=%v", kin, kout, size, key, got, want)
			}
		case opKeys:
			if got, want := tq.Keys(), ora.keys(); !slices.Equal(got, want) {
				t.Fatalf("kin=%d kout=%d size=%d Keys mismatch: real=%v oracle=%v", kin, kout, size, got, want)
			}
		case opResetStats:
			if got := tq.ResetStats(); got != want {
				t.Fatalf("kin=%d kout=%d size=%d ResetStats returned %+v, want %+v", kin, kout, size, got, want)
			}
			want = Stats{}
		}

		if got := tq.Stats(); got != want {
			t.Fatalf("kin=%d kout=%d size=%d Stats after op %d on key %d: %+v, want %+v", kin, kout, size, op, key, got, want)
		}

		checkIndex(t, tq)

		if tq.Len() != ora.length() {
			t.Fatalf("kin=%d kout=%d size=%d Len mismatch after op %d on key %d: real=%d oracle=%d", kin, kout, size, op, key, tq.Len(), ora.length())
		}
	}
}

func FuzzTwoQueueAgainstOracle(f *testing.F) {
	f.Add(2, 2, 2, []byte{1, 0, 1, 1, 1, 2, 1, 3, 1, 4, 0, 0, 3, 1})
	f.Add(0, 0, 0, []byte{1, 0, 1, 1})
	f.Add(4, 4, 4, []byte{1, 0, 1, 1, 1, 2, 1, 3, 1, 4, 1, 0, 2, 0, 3, 1, 1, 5})

	f.Fuzz(func(t *testing.T, kinByte, koutByte, sizeByte int, ops []byte) {
		if len(ops) > 4000 {
			ops = ops[:4000]
		}
		kin := int(uint(kinByte) % 9)
		kout := int(uint(koutByte) % 9)
		size := int(uint(sizeByte) % 9)
		runTwoQueueSequence(t, kin, kout, size, ops)
	})
}

func TestTwoQueueProperty(t *testing.T) {
	type cfg struct{ kin, kout, size int }
	for _, c := range []cfg{
		{0, 0, 0}, {1, 0, 0}, {0, 1, 1}, {1, 1, 1},
		{2, 2, 2}, {4, 4, 4}, {2, 4, 8}, {8, 2, 4}, {8, 8, 8},
		{16, 32, 48}, {32, 64, 96},
	} {
		t.Run(fmt.Sprintf("kin=%d/kout=%d/size=%d", c.kin, c.kout, c.size), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(c.kin*97 + c.kout*13 + c.size + 1)))
			ops := make([]byte, 200000)
			r.Read(ops)
			runTwoQueueSequence(t, c.kin, c.kout, c.size, ops)
		})
	}
}

// TestTwoQueue_NeverUsedSlotDoesNotAliasZeroKey is a regression test for the
// bug fixed by internal/ring: evicting a never-used slot (key defaults to
// K's zero value) must not delete a real entry legitimately stored under
// that same zero value elsewhere in the cache.
func TestTwoQueue_NeverUsedSlotDoesNotAliasZeroKey(t *testing.T) {
	l := NewParams[int, string](2, 2, 2)

	if e := l.Set(0, "real-zero-key"); e != nil {
		t.Fatalf("first insert should not evict: %+v", e)
	}
	if e := l.Set(1, "second"); e != nil {
		t.Fatalf("second insert (into a never-used slot) should not evict: %+v", e)
	}

	if v := l.Get(0); v == nil || *v != "real-zero-key" {
		t.Fatalf("key 0 must still be retrievable, got %v", v)
	}
}
