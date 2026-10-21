# 2q
[![Go Reference](https://pkg.go.dev/badge/github.com/d1n-go/2q.svg)](https://pkg.go.dev/github.com/d1n-go/2q)
[![CI](https://github.com/d1n-go/2q/actions/workflows/ci.yml/badge.svg)](https://github.com/d1n-go/2q/actions/workflows/ci.yml)
[![Fuzz](https://github.com/d1n-go/2q/actions/workflows/fuzz.yml/badge.svg)](https://github.com/d1n-go/2q/actions/workflows/fuzz.yml)
[![codecov](https://codecov.io/gh/d1n-go/2q/branch/main/graph/badge.svg)](https://codecov.io/gh/d1n-go/2q)
[![License: MIT](https://img.shields.io/github/license/d1n-go/2q)](LICENSE)

Thread safe GoLang [2Q](http://www.vldb.org/conf/1994/P439.PDF) cache.

Maintained fork of the archived [floatdrop/2q](https://github.com/floatdrop/2q).
Its former dependencies (`floatdrop/lru`, `floatdrop/fifo`, both also archived)
have been folded in: their `bahlo/generic-list-go` based linked lists became a
preallocated slice-backed ring (`internal/ring`), and their per-queue maps a
single key index shared by all three queues — so this module has zero
dependencies outside the standard library and is about twice as fast as
upstream (see [Benchmarks](#benchmarks)). The upstream
API is kept as is and extended (see [API](#api)); see
[NOTICE.md](NOTICE.md) for attribution.

### Differences from upstream

- **All operations are atomic.** Upstream locked each internal queue
  separately, so two goroutines writing the same key could leave a copy of it
  in both the recent and the frequent queue; `Remove` then deleted only one
  and `Get` resurrected the other. `TwoQueue` now holds a single cache-wide
  lock.
- **No lost entries on slot reuse.** Upstream's `lru`/`fifo` evicted a slot by
  unconditionally deleting the index entry for whatever key that slot last
  held, even when the slot was empty. If the same key was live in another
  slot, the live entry silently vanished. This is reachable via `Remove`, via
  the zero value of `K`, and — through the ghost queue — from a plain
  `Set`-only workload, where it made upstream "forget" recently evicted keys
  and skip promotions it should have made. The fork only clears the index
  for a slot that actually held a value, so eviction decisions can differ
  from upstream in exactly those situations.
- **`Set` reports every value it replaces.** Upstream returned the previous
  value when updating a key in the frequent queue, but `nil` when updating a
  key in the recent queue, silently dropping the old value. The fork returns
  the previous value in both cases, so a non-nil result always means "this
  value is no longer in the cache" (see [API](#api)).

The [`compat`](compat) module runs the fork in lockstep with the archived
upstream packages against a slot-level model of their shared storage and
checks that the last two are the *only* single-threaded behavioral
differences; it is a separate module so the root stays dependency-free. The
first one only shows under concurrency, which a lockstep comparison cannot
observe: it holds by construction (every method runs entirely under the one
lock), and the concurrency tests check its invariants under `-race`.

### Relation to the paper

`2q_paper_test.go` carries a line-by-line transcription of the "2Q Full
Version" pseudocode from the [paper](http://www.vldb.org/conf/1994/P439.PDF)
and replays access traces against it. The queues and transitions are the
paper's, with one structural difference inherited from upstream: in the paper
A1in and Am draw from **one shared pool** of slots and `Kin` is a threshold on
|A1in|, so nothing is evicted while the pool has free slots and |A1in| drifts
between `Kin` and `Kin+1` in steady state. Here A1in and Am are **separate
fixed-size queues** — A1in pages out its tail as soon as it holds `Kin`
entries, even if Am is empty, and Am's size never changes. The simulator has a
switch for the pool policy: the cache matches it exactly under the
fixed-partition policy (fuzzed), and a test pins the first point of divergence
from the paper's policy (the `Kin+1`-th insertion into a fresh cache). On an
80/20 trace the measured hit-rate cost of the fixed partition is under half a
percentage point; `go test -run HitRate -v` prints the numbers.

## Example

```go
import (
	"fmt"

	twoqueue "github.com/d1n-go/2q"
)

func main() {
	cache := twoqueue.New[string, int](256)

	cache.Set("Hello", 5)

	if e := cache.Get("Hello"); e != nil {
		fmt.Println(*e)
		// Output: 5
	}
}
```

## API

| Method | |
|---|---|
| `New(size)`, `NewParams(Kin, Kout, size)` | Create a cache; `New` splits `size` 25% / 50% / 75% into A1in / A1out (ghost keys) / Am. Capacities above `MaxSize` panic. |
| `Get(key) *V` | Look up a value; refreshes recency in Am. Counted in `Stats`. |
| `Peek(key) *V`, `Contains(key) bool` | Look up without touching recency or `Stats`. |
| `Set(key, value) *Evicted[K, V]` | Store a value; see below for the result. |
| `Remove(key) *V` | Delete an entry and return its value. |
| `Len()`, `Keys()` | Number of cached keys; a snapshot of them (Am most to least recently used, then A1in newest to oldest). |
| `Purge()` | Drop every entry and ghost key, keeping the allocated storage. |
| `Stats()`, `ResetStats()` | Hit/miss counters of `Get`, with `HitRate()`. |

`Set` returns the key/value pair the cache stopped holding because of that
call, or `nil`:

- updating a cached key returns the key with its **previous** value (the key
  stays cached with the new one);
- storing a key that does not fit returns the **evicted** entry;
- with a zero-capacity queue the pair just passed in can come back, because
  it was not stored at all.

A non-nil result is therefore the place to release whatever a value holds
(close a handle, return a buffer to a pool). The one caveat: re-setting a key
to the value it already has returns that same, still cached value.

`Get`, `Peek` and `Remove` return pointers. A pointer stays valid after the
entry is updated or evicted (the cache never writes through it), but writing
through it yourself is not synchronized by the cache.

Every method takes a single cache-wide mutex, `Get` included, because 2Q
updates recency on reads. Under heavy concurrent access one cache does not
just stop scaling, its total throughput **drops**: the cores spend their time
handing the mutex's cache line back and forth. On an M1 Pro, `Get` on a warm
cache takes about 14 ns from one goroutine and about 150 ns per operation
(total, not per goroutine) from eight, i.e. roughly a tenth of the
throughput; `go test -run '^$' -bench ParallelGet -cpu 1,4,8` reproduces it.

This only matters for a cache hit millions of times per second from many
cores; next to a database or network round trip it is noise. Check a mutex
profile first. If it does matter, shard: route each key by hash to one of N
independent caches of size/N. With 64 shards and eight goroutines the same
`Get` workload ran at about 20 ns/op, and a 90% `Get` / 10% `Set` mix at
about 45 ns/op instead of 250. Each shard is its own 2Q, so eviction is
per shard rather than global, which with a decent hash is statistically
close.

## TTL

The cache has no built-in expiry. You can wrap values into an `Expiring[T]`
that releases its payload on a timer; `Valid` then returns `nil` once the TTL
has passed. The cache entry itself stays until it is evicted normally — only
the memory behind it is freed. This trades one `time.AfterFunc` timer per
entry for eager release; if you only need lazy expiry, storing a deadline next
to the value and comparing it in `Valid` is cheaper.

The example below is compiled and race-tested in
[`example_ttl_test.go`](example_ttl_test.go).

<details>
    <summary>Example implementation</summary>

```go
import (
    "fmt"
    "sync/atomic"
    "time"

    twoqueue "github.com/d1n-go/2q"
)

// Expiring wraps a value that releases itself after a TTL. The pointer
// is held in a shared atomic box, so every copy of an Expiring (the one
// stored in the cache, the one the timer closure captured) sees the same
// state, and the timer goroutine can clear it while readers Load it
// without a data race.
type Expiring[T any] struct {
    value *atomic.Pointer[T]
}

// Valid returns the value, or nil once the TTL has passed. It is nil-safe
// so it can be chained directly onto a cache Get.
func (e *Expiring[T]) Valid() *T {
    if e == nil {
        return nil
    }
    return e.value.Load()
}

func WithTTL[T any](value T, ttl time.Duration) Expiring[T] {
    e := Expiring[T]{value: new(atomic.Pointer[T])}
    e.value.Store(&value)

    time.AfterFunc(ttl, func() {
        e.value.Store(nil) // Release memory; the cache entry itself stays until evicted.
    })

    return e
}

func main() {
    cache := twoqueue.New[string, Expiring[string]](256)

    cache.Set("Hello", WithTTL("Bye", time.Hour))

    if v := cache.Get("Hello").Valid(); v != nil {
        fmt.Println(*v)
        // Output: Bye
    }
}
```

</details>

## Benchmarks

Measured against the original `floatdrop/2q` v0.1.1 + `floatdrop/lru` v1.2.1 + `floatdrop/fifo` v0.1.1 (last released versions, before archiving) with the same benchmark code (`Benchmark2Q_Rand`, `Benchmark2Q_Freq` in `2q_test.go`), `benchstat`, `-count=10`, Apple M1 Pro:

```
          │  upstream   │                fork                 │
          │   sec/op    │   sec/op     vs base                │
2Q_Rand-8   286.4n ± 1%   151.5n ± 1%  -47.10% (p=0.000 n=10)
2Q_Freq-8   263.2n ± 1%   140.1n ± 1%  -46.78% (p=0.000 n=10)

          │    B/op    │    B/op     vs base                │
2Q_Rand-8   46.00 ± 0%   30.00 ± 0%  -34.78% (p=0.000 n=10)
2Q_Freq-8   44.00 ± 0%   29.00 ± 0%  -34.09% (p=0.000 n=10)

          │ allocs/op  │ allocs/op   vs base                │
2Q_Rand-8   3.000 ± 0%   2.000 ± 0%  -33.33% (p=0.000 n=10)
2Q_Freq-8   3.000 ± 0%   2.000 ± 0%  -33.33% (p=0.000 n=10)
```

Most of the gain comes from the shared key index: upstream (and this fork up to v0.5.0) kept one map per queue, so a `Set` of a new key into full queues probed all three maps and then updated two of them, about nine map operations; with one index mapping a key to its queue and slot it is four, and every `Get` is a single lookup. The remaining allocations are the value box that keeps pointers returned by `Get` valid, and the `Evicted` that `Set` returns. The cache's own memory is unchanged to slightly smaller (one map instead of three).

These are single-goroutine numbers; see [API](#api) for what happens under contention.

## Correctness

Four differential fuzz targets check the implementation against independent, purpose-built reference models (plain slices, no shared code with the real implementation):

- `internal/ring/ring_fuzz_test.go` — `Ring` against a slice model, plus self-consistency checks on the ring's linked list, to guard against the class of bug a hand-rolled ring buffer can introduce: silent data loss or aliasing on wraparound;
- `2q_fuzz_test.go` — the whole `TwoQueue` API against a slice-based 2Q oracle;
- `2q_paper_test.go` — access traces against a transcription of the paper's pseudocode (see [Relation to the paper](#relation-to-the-paper));
- `compat/upstream_test.go` — the fork and upstream against a slot-level model (see [Differences from upstream](#differences-from-upstream)).

Their seed corpora and fixed-seed property variants run as regular tests in CI on every push (with `-race`, and on a 32-bit `GOARCH=386` build), and a [scheduled workflow](.github/workflows/fuzz.yml) additionally fuzzes all four targets for 5 minutes daily with a corpus that persists across runs.

For small caches, `2q_exhaustive_test.go` goes further than sampling: it model-checks `TwoQueue` against the same oracle by visiting **every** state a cache can reach (identified slot by slot, empty slots and links included) and applying every operation to each, so it covers operation sequences of any length. At each step the result must match the oracle and the queues must hold exactly the oracle's lists, ghost queue and order included. It runs on every push for every capacity split up to 2 per queue with a total of at most 4, over one key more than the queues hold; the scheduled workflow also runs the rest up to 2/2/2 (`-exhaustive`, 1.6 million states). This is a proof only within those bounds: a bug that needs a larger queue is left to the fuzz and property tests above.

