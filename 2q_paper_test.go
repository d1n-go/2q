package twoqueue

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// sim2Q is a transcription of the 2Q "Full Version" pseudocode from
// Johnson & Shasha, "2Q: A Low Overhead High Performance Buffer
// Management Replacement Algorithm", VLDB 1994, section 2.2. Queues hold
// keys, head first; nothing here is shared with the implementation.
//
// The paper's A1in and Am draw page slots from one shared pool of size
// N, and Kin is a threshold on |A1in| that decides where reclaimfor
// takes a slot from once the pool is full:
//
//	reclaimfor(page X)
//	    if there are free page slots then
//	        put X into a free page slot
//	    else if (|A1in| > Kin)
//	        page out the tail of A1in, call it Y
//	        add identifier of Y to the head of A1out
//	        if (|A1out| > Kout) remove identifier of Z from the tail of A1out
//	        put X into the reclaimed page slot
//	    else
//	        page out the tail of Am, call it Y   // do not put it on A1out
//	        put X into the reclaimed page slot
//
//	On accessing a page X:
//	    if X is in Am then          move X to the head of Am
//	    else if X is in A1out then  reclaimfor(X); add X to the head of Am
//	    else if X is in A1in then   // do nothing
//	    else                        reclaimfor(X); add X to the head of A1in
//
// TwoQueue (like the upstream it forks, and unlike e.g. hashicorp's 2Q)
// keeps the same three queues and the same transitions, but gives A1in
// and Am fixed, separate capacities: A1in holds exactly Kin entries and
// evicts its tail as soon as it is full, whether or not Am has room, and
// promoting into a full Am evicts Am's tail without consulting |A1in|.
// poolPolicy selects between the two; every other line is identical.
type poolPolicy int

const (
	// sharedPool is the paper: N slots shared by A1in and Am, Kin a threshold.
	sharedPool poolPolicy = iota
	// partitioned is this library: A1in has Kin slots, Am has amSize slots.
	partitioned
)

type sim2Q struct {
	policy          poolPolicy
	n, kin, kout    int // n = kin + amSize
	amSize          int
	a1in, a1out, am []int
}

func newSim2Q(policy poolPolicy, kin, kout, amSize int) *sim2Q {
	return &sim2Q{policy: policy, n: kin + amSize, kin: kin, kout: kout, amSize: amSize}
}

func indexOf(s []int, x int) int {
	for i, v := range s {
		if v == x {
			return i
		}
	}
	return -1
}

func without(s []int, i int) []int  { return append(s[:i:i], s[i+1:]...) }
func pushHead(s []int, x int) []int { return append([]int{x}, s...) }
func popTail(s []int) ([]int, int)  { return s[:len(s)-1], s[len(s)-1] }

const noEviction = -1

func (s *sim2Q) pageOutA1inTail() int {
	var y int
	s.a1in, y = popTail(s.a1in)
	s.a1out = pushHead(s.a1out, y)
	if len(s.a1out) > s.kout {
		s.a1out, _ = popTail(s.a1out)
	}
	return y
}

func (s *sim2Q) pageOutAmTail() int {
	var y int
	s.am, y = popTail(s.am)
	return y
}

// reclaimfor frees a slot for a page about to be added to queue `into`
// (a1in or am) and returns the key paged out, or noEviction.
func (s *sim2Q) reclaimfor(into *[]int) int {
	switch s.policy {
	case sharedPool:
		if len(s.a1in)+len(s.am) < s.n {
			return noEviction
		}
		if len(s.a1in) > s.kin {
			return s.pageOutA1inTail()
		}
		return s.pageOutAmTail()

	case partitioned:
		if into == &s.a1in {
			if len(s.a1in) < s.kin {
				return noEviction
			}
			return s.pageOutA1inTail()
		}
		if len(s.am) < s.amSize {
			return noEviction
		}
		return s.pageOutAmTail()
	}
	panic("unknown pool policy")
}

// access replays "On accessing a page X" and reports whether X was
// already resident and which page (if any) was evicted to make room.
//
// Two zero-capacity corner cases have no analogue in the paper (a queue
// of maximum size 0 makes no sense for page slots) and are modelled the
// way TwoQueue behaves so the partitioned variant stays comparable:
// with Kin == 0 a new page is paged out to A1out at once, and with
// amSize == 0 a promoted page is dropped at once. Under sharedPool these
// cannot arise because n == 0 has no slots to reclaim at all.
func (s *sim2Q) access(x int) (hit bool, evicted int) {
	if i := indexOf(s.am, x); i >= 0 {
		s.am = pushHead(without(s.am, i), x)
		return true, noEviction
	}
	if i := indexOf(s.a1out, x); i >= 0 {
		if s.policy == partitioned {
			s.a1out = without(s.a1out, i) // TwoQueue drops the ghost on promotion
			if s.amSize == 0 {
				return false, x
			}
		}
		evicted = s.reclaimfor(&s.am)
		s.am = pushHead(s.am, x)
		return false, evicted
	}
	if indexOf(s.a1in, x) >= 0 {
		return true, noEviction
	}
	if s.policy == partitioned && s.kin == 0 {
		s.a1out = pushHead(s.a1out, x)
		if len(s.a1out) > s.kout {
			s.a1out, _ = popTail(s.a1out)
		}
		return false, x
	}
	evicted = s.reclaimfor(&s.a1in)
	s.a1in = pushHead(s.a1in, x)
	return false, evicted
}

func (s *sim2Q) resident() int { return len(s.a1in) + len(s.am) }

// accessTwoQueue is the paper's "access" expressed through the cache's
// API: a miss is followed by a Set of the same key, the way a caller
// loading a value on a miss would do it.
func accessTwoQueue(c *TwoQueue[int, int], x int) (hit bool, evicted int) {
	if c.Get(x) != nil {
		return true, noEviction
	}
	if e := c.Set(x, x); e != nil {
		return false, e.Key
	}
	return false, noEviction
}

// runAccessTrace replays a key trace against TwoQueue and the
// partitioned simulator in lockstep and fails on the first step where
// hit/miss, the evicted key, or the number of resident keys differ.
func runAccessTrace(t *testing.T, kin, kout, size int, keys []byte) {
	t.Helper()

	const keyDomain = 8
	c := NewParams[int, int](kin, kout, size)
	s := newSim2Q(partitioned, kin, kout, size)

	for i, b := range keys {
		x := int(b) % keyDomain
		ch, ce := accessTwoQueue(c, x)
		sh, se := s.access(x)
		if ch != sh || ce != se {
			t.Fatalf("kin=%d kout=%d size=%d access #%d key %d: TwoQueue (hit=%v evicted=%d) vs simulator (hit=%v evicted=%d)",
				kin, kout, size, i, x, ch, ce, sh, se)
		}
		if c.Len() != s.resident() {
			t.Fatalf("kin=%d kout=%d size=%d access #%d key %d: Len()=%d but simulator has %d resident",
				kin, kout, size, i, x, c.Len(), s.resident())
		}
	}
}

func FuzzTwoQueueAgainstPaperSimulator(f *testing.F) {
	f.Add(1, 2, 3, []byte{0, 1, 2, 3, 0, 1, 4, 5, 0, 6, 7, 1})
	f.Add(0, 2, 2, []byte{0, 0, 1, 1, 0})
	f.Add(2, 2, 0, []byte{0, 1, 2, 0, 0})

	f.Fuzz(func(t *testing.T, kinByte, koutByte, sizeByte int, keys []byte) {
		if len(keys) > 4000 {
			keys = keys[:4000]
		}
		kin, kout, size := int(uint(kinByte)%5), int(uint(koutByte)%5), int(uint(sizeByte)%5)
		runAccessTrace(t, kin, kout, size, keys)
	})
}

func TestTwoQueueAgainstPaperSimulatorProperty(t *testing.T) {
	type cfg struct{ kin, kout, size int }
	for _, c := range []cfg{
		{0, 0, 0}, {1, 0, 0}, {0, 1, 1}, {1, 1, 1}, {0, 2, 2}, {2, 2, 0},
		{1, 2, 3}, {2, 4, 6}, {4, 4, 4}, {3, 1, 5},
	} {
		c := c
		t.Run(fmt.Sprintf("kin=%d/kout=%d/size=%d", c.kin, c.kout, c.size), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(c.kin*31 + c.kout*7 + c.size + 1)))
			keys := make([]byte, 10000)
			r.Read(keys)
			runAccessTrace(t, c.kin, c.kout, c.size, keys)
		})
	}
}

// TestPaperSharedPoolDivergesAtWarmup pins down exactly where TwoQueue
// departs from the paper's shared-pool version: inserting distinct keys
// into a fresh cache, both agree for the first Kin insertions; on the
// (Kin+1)-th the paper still has free slots (Am is empty) and evicts
// nothing, while TwoQueue's A1in is full and pages out its tail.
func TestPaperSharedPoolDivergesAtWarmup(t *testing.T) {
	type cfg struct{ kin, kout, size int }
	for _, c := range []cfg{{1, 2, 3}, {2, 4, 6}, {25, 50, 75}} {
		cache := NewParams[int, int](c.kin, c.kout, c.size)
		paper := newSim2Q(sharedPool, c.kin, c.kout, c.size)

		for x := 1; x <= c.kin; x++ {
			ch, ce := accessTwoQueue(cache, x)
			ph, pe := paper.access(x)
			if ch || ph || ce != noEviction || pe != noEviction {
				t.Fatalf("%+v: insertion %d should be a miss without eviction on both sides", c, x)
			}
		}

		x := c.kin + 1
		if _, pe := paper.access(x); pe != noEviction {
			t.Fatalf("%+v: paper has %d free slots and must not evict on insertion %d, evicted %d", c, c.size, x, pe)
		}
		if _, ce := accessTwoQueue(cache, x); ce != 1 {
			t.Fatalf("%+v: TwoQueue's A1in is full and must page out key 1 on insertion %d, evicted %d", c, x, ce)
		}
		if paper.resident() != cache.Len()+1 {
			t.Fatalf("%+v: paper keeps %d resident, TwoQueue %d", c, paper.resident(), cache.Len())
		}
	}
}

// TestPaperVsTwoQueueHitRate reports (does not assert) how the two
// variants compare on a skewed trace, so the cost of the fixed partition
// can be seen in numbers. Run with -v.
func TestPaperVsTwoQueueHitRate(t *testing.T) {
	if testing.Short() {
		t.Skip("simulator is O(n) per access")
	}

	const (
		accesses = 100000
		keys     = 10000
	)
	// 80/20 skew: the most popular 20% of keys get 80% of the accesses,
	// i.e. P(key <= i) = (i/keys)^theta with theta = log 0.8 / log 0.2.
	theta := math.Log(0.8) / math.Log(0.2)
	r := rand.New(rand.NewSource(42))
	trace := make([]int, accesses)
	for i := range trace {
		trace[i] = int(math.Ceil(math.Pow(r.Float64(), 1/theta) * keys))
	}

	for _, size := range []int{250, 500, 1000} {
		kin, kout, am := size/4, size/2, size-size/4
		cache := NewParams[int, int](kin, kout, am)
		paper := newSim2Q(sharedPool, kin, kout, am)
		var hc, hp int
		for _, x := range trace {
			if h, _ := accessTwoQueue(cache, x); h {
				hc++
			}
			if h, _ := paper.access(x); h {
				hp++
			}
		}
		t.Logf("slots=%d (Kin=%d Kout=%d Am=%d): hit rate paper=%.4f TwoQueue=%.4f",
			size, kin, kout, am, float64(hp)/accesses, float64(hc)/accesses)
	}
}
