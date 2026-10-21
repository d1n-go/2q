package twoqueue

import (
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/d1n-go/2q/internal/ring"
)

// The test below is a bounded model check of TwoQueue against the oracle.
// Instead of sampling operation sequences like the fuzz tests, it
// enumerates every state a cache of the given capacities can reach over a
// key domain, breadth first, and applies every operation to each of them.
// Since TwoQueue is deterministic and a state is identified by all of its
// slots (key, value, links, empty ones included), the reachable set is
// finite, and once it is exhausted every sequence of operations of any
// length has been checked: each step of it starts from a visited state.
//
// At every step the oracle must agree on the result (stepTwoQueue), and
// the cache's queues, read slot by slot, must hold exactly the oracle's
// lists in the same order (abstractionError). The latter also covers what
// no public method shows directly: the ghost queue and the FIFO order of
// A1in.
//
// Stats are left out of a state, since they only count up; stepTwoQueue
// still checks every increment and every ResetStats against the oracle.

// exploreOp is one transition: an operation, its key and the value Set
// stores.
type exploreOp struct {
	op       byte
	key, val int
}

func (o exploreOp) String() string {
	switch o.op {
	case opGet:
		return fmt.Sprintf("Get(%d)", o.key)
	case opSet:
		return fmt.Sprintf("Set(%d,%d)", o.key, o.val)
	case opPeek:
		return fmt.Sprintf("Peek(%d)", o.key)
	case opRemove:
		return fmt.Sprintf("Remove(%d)", o.key)
	case opPurge:
		return "Purge()"
	case opContains:
		return fmt.Sprintf("Contains(%d)", o.key)
	case opKeys:
		return "Keys()"
	default:
		return "ResetStats()"
	}
}

// exploreOps lists every operation over keys 0..keys-1. Each key gets two
// values of its own (2k and 2k+1), so a stale value, or one belonging to
// another key, can never pass for the right one.
func exploreOps(keys int) []exploreOp {
	ops := []exploreOp{{op: opPurge}, {op: opKeys}, {op: opResetStats}}
	for k := range keys {
		ops = append(ops,
			exploreOp{op: opGet, key: k},
			exploreOp{op: opSet, key: k, val: 2 * k},
			exploreOp{op: opSet, key: k, val: 2*k + 1},
			exploreOp{op: opPeek, key: k},
			exploreOp{op: opRemove, key: k},
			exploreOp{op: opContains, key: k},
		)
	}
	return ops
}

func cloneTwoQueue(l *TwoQueue[int, int]) *TwoQueue[int, int] {
	return &TwoQueue[int, int]{
		index:       maps.Clone(l.index),
		recent:      l.recent.Clone(),
		recentEvict: l.recentEvict.Clone(),
		frequent:    l.frequent.Clone(),
		stats:       l.stats,
	}
}

func cloneOracle(o *oracle2Q) *oracle2Q {
	return &oracle2Q{
		recent:      oracleFIFO{cap: o.recent.cap, list: slices.Clone(o.recent.list)},
		recentEvict: oracleFIFO{cap: o.recentEvict.cap, list: slices.Clone(o.recentEvict.list)},
		frequent:    oracleLRU{cap: o.frequent.cap, list: slices.Clone(o.frequent.list)},
	}
}

// walkRing calls visit for every slot of r from front to back, empty ones
// included, and reports a broken ring: a prev link that does not mirror
// next, or a walk that does not come back to the sentinel after exactly
// Cap() slots.
func walkRing[W any](r *ring.Ring[int, W], visit func(slot int32)) error {
	sentinel := int32(r.Cap())
	i, n := sentinel, 0
	for {
		next := r.Next(i)
		if r.Prev(next) != i {
			return fmt.Errorf("slot %d: next is %d, whose prev is %d", i, next, r.Prev(next))
		}
		if next == sentinel {
			break
		}
		if n++; n > r.Cap() {
			return fmt.Errorf("walk from the sentinel passes more than %d slots", r.Cap())
		}
		visit(next)
		i = next
	}
	if n != r.Cap() {
		return fmt.Errorf("walk from the sentinel reaches %d of %d slots", n, r.Cap())
	}
	return nil
}

// encodeRing appends every slot of r, front to back, as three bytes: slot,
// key and value+1 (0 for an empty slot). Two caches therefore encode alike
// exactly when their queues are identical slot for slot. Slots, keys and
// values must stay below 255.
func encodeRing[W any](b []byte, r *ring.Ring[int, W], value func(*W) int) ([]byte, error) {
	err := walkRing(r, func(slot int32) {
		v := 0
		if p := r.Value(slot); p != nil {
			v = value(p) + 1
		}
		b = append(b, byte(slot), byte(r.Key(slot)), byte(v))
	})
	return append(b, 0xff), err
}

func encodeTwoQueue(b []byte, l *TwoQueue[int, int]) ([]byte, error) {
	val := func(v *int) int { return *v }
	ghost := func(*struct{}) int { return 0 }
	b, err := encodeRing(b, l.recent, val)
	if err != nil {
		return b, fmt.Errorf("recent: %w", err)
	}
	if b, err = encodeRing(b, l.recentEvict, ghost); err != nil {
		return b, fmt.Errorf("recentEvict: %w", err)
	}
	if b, err = encodeRing(b, l.frequent, val); err != nil {
		return b, fmt.Errorf("frequent: %w", err)
	}
	return b, nil
}

// occupiedEntries lists the occupied slots of r from front to back in the
// oracle's form.
func occupiedEntries[W any](r *ring.Ring[int, W], value func(*W) int) []oracleEntry {
	var es []oracleEntry
	_ = walkRing(r, func(slot int32) { // encodeTwoQueue reports a broken ring
		if p := r.Value(slot); p != nil {
			es = append(es, oracleEntry{r.Key(slot), value(p)})
		}
	})
	return es
}

// abstractionError reports whether the queues of l, read slot by slot from
// front to back, hold exactly the oracle's three lists.
func abstractionError(l *TwoQueue[int, int], o *oracle2Q) error {
	val := func(v *int) int { return *v }
	ghost := func(*struct{}) int { return 0 }
	for _, q := range []struct {
		name      string
		got, want []oracleEntry
	}{
		{"recent", occupiedEntries(l.recent, val), o.recent.list},
		{"recentEvict", occupiedEntries(l.recentEvict, ghost), o.recentEvict.list},
		{"frequent", occupiedEntries(l.frequent, val), o.frequent.list},
	} {
		if !slices.Equal(q.got, q.want) {
			return fmt.Errorf("%s holds %v, oracle %v", q.name, q.got, q.want)
		}
	}
	return nil
}

type exploreState struct {
	tq   *TwoQueue[int, int]
	ora  *oracle2Q
	want Stats
	id   int
}

// readOnly reports whether op must leave the queues untouched. Those ops
// run on the state itself rather than a copy, which must then still
// encode as before.
func readOnly(op byte) bool {
	return op == opPeek || op == opContains || op == opKeys
}

// exploreTwoQueue visits every state reachable from an empty cache with
// the given capacities over keys 0..keys-1 and checks every operation
// from each. It returns the number of distinct states.
func exploreTwoQueue(t *testing.T, kin, kout, size, keys int) int {
	t.Helper()

	if 2*keys >= 0xff || kin >= 0xff || kout >= 0xff || size >= 0xff {
		t.Fatalf("kin=%d kout=%d size=%d keys=%d do not fit the byte encoding", kin, kout, size, keys)
	}
	fail := func(path string, op exploreOp, err error) {
		t.Helper()
		t.Fatalf("kin=%d kout=%d size=%d keys=%d: after [%s] then %v: %v", kin, kout, size, keys, path, op, err)
	}

	ops := exploreOps(keys)
	// parent and via record how each state was first reached, to print
	// the path to a failing step.
	parent, via := []int{-1}, []exploreOp{{}}
	path := func(id int) string {
		var steps []string
		for ; id > 0; id = parent[id] {
			steps = append(steps, via[id].String())
		}
		slices.Reverse(steps)
		return strings.Join(steps, " ")
	}

	root := NewParams[int, int](kin, kout, size)
	enc, err := encodeTwoQueue(nil, root)
	if err != nil {
		t.Fatalf("kin=%d kout=%d size=%d: fresh cache: %v", kin, kout, size, err)
	}
	seen := map[string]bool{string(enc): true}
	frontier := []exploreState{{tq: root, ora: newOracle2Q(kin, kout, size)}}

	var before, after []byte
	for len(frontier) > 0 {
		s := frontier[0]
		frontier[0] = exploreState{}
		frontier = frontier[1:]
		if before, err = encodeTwoQueue(before[:0], s.tq); err != nil {
			fail(path(s.id), exploreOp{}, err)
		}

		for _, op := range ops {
			tq, ora, want := s.tq, s.ora, s.want
			if !readOnly(op.op) {
				tq, ora = cloneTwoQueue(tq), cloneOracle(ora)
			}
			err := stepTwoQueue(tq, ora, &want, op.op, op.key, op.val)
			if err == nil {
				err = abstractionError(tq, ora)
			}
			if err == nil {
				after, err = encodeTwoQueue(after[:0], tq)
			}
			if err == nil && readOnly(op.op) && string(after) != string(before) {
				err = fmt.Errorf("a read-only operation changed the queues")
			}
			if err != nil {
				fail(path(s.id), op, err)
			}
			if seen[string(after)] {
				continue
			}
			seen[string(after)] = true
			parent, via = append(parent, s.id), append(via, op)
			frontier = append(frontier, exploreState{tq, ora, want, len(parent) - 1})
		}
	}
	return len(seen)
}

var exhaustiveAll = flag.Bool("exhaustive", false,
	"make TestTwoQueueExhaustive check every capacity up to 2 per queue (about a minute) instead of a total capacity up to 4")

// TestTwoQueueExhaustive model-checks every combination of capacities up
// to 2 per queue whose total is at most 4, over one more key than the
// queues can hold together, so every queue can fill up and evict while
// another key is still outside. With -exhaustive it also checks the
// totals of 5 and 6, which take about a minute: 2/2/2 alone has 1.6
// million states.
func TestTwoQueueExhaustive(t *testing.T) {
	for kin := range 3 {
		for kout := range 3 {
			for size := range 3 {
				if kin+kout+size > 4 && !*exhaustiveAll {
					continue
				}
				keys := max(2, kin+kout+size+1)
				t.Run(fmt.Sprintf("kin=%d/kout=%d/size=%d", kin, kout, size), func(t *testing.T) {
					n := exploreTwoQueue(t, kin, kout, size, keys)
					t.Logf("%d keys: %d states", keys, n)
				})
			}
		}
	}
}
