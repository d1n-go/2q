# 2q
[![Go Reference](https://pkg.go.dev/badge/github.com/d1n-go/2q.svg)](https://pkg.go.dev/github.com/d1n-go/2q)
[![CI](https://github.com/d1n-go/2q/actions/workflows/ci.yml/badge.svg)](https://github.com/d1n-go/2q/actions/workflows/ci.yml)
[![Fuzz](https://github.com/d1n-go/2q/actions/workflows/fuzz.yml/badge.svg)](https://github.com/d1n-go/2q/actions/workflows/fuzz.yml)
[![codecov](https://codecov.io/gh/d1n-go/2q/branch/main/graph/badge.svg)](https://codecov.io/gh/d1n-go/2q)
[![License: MIT](https://img.shields.io/github/license/d1n-go/2q)](LICENSE)

Thread safe GoLang [2Q](http://www.vldb.org/conf/1994/P439.PDF) cache.

Maintained fork of the archived [floatdrop/2q](https://github.com/floatdrop/2q).
Its former dependencies (`floatdrop/lru`, `floatdrop/fifo`, both also archived)
have been folded in as internal packages, and their `bahlo/generic-list-go`
based linked list was replaced by a slice-backed ring (`internal/ring`) — so
this module has zero dependencies outside the standard library. Public API is
unchanged. See [NOTICE.md](NOTICE.md) for attribution.

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

The [`compat`](compat) module runs the fork in lockstep with the archived
upstream packages against a slot-level model of their shared storage and
checks that this is the *only* behavioral difference; it is a separate module
so the root stays dependency-free.

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

## TTL

You can wrap values into an `Expiring[T any]` struct to release memory on a timer (or manually, in a `Valid` method).

<details>
    <summary>Example implementation</summary>

```go
import (
    "fmt"
    "time"

    twoqueue "github.com/d1n-go/2q"
)

type Expiring[T any] struct {
    value *T
}

func (E *Expiring[T]) Valid() *T {
    if E == nil {
        return nil
    }

    return E.value
}

func WithTTL[T any](value T, ttl time.Duration) Expiring[T] {
    e := Expiring[T]{
        value: &value,
    }

    time.AfterFunc(ttl, func() {
        e.value = nil // Release memory
    })

    return e
}

func main() {
    cache := twoqueue.New[string, Expiring[string]](256)

    cache.Set("Hello", WithTTL("Bye", time.Hour))

    if e := cache.Get("Hello").Valid(); e != nil {
        fmt.Println(*e)
    }
}
```

**Note:** although this short implementation frees memory after the TTL duration, it does not erase the entry for the key in the cache. It can be a problem if you do not check nilness after getting an element from the cache and call `Set` afterwards.
</details>

## Benchmarks

Measured against the original `floatdrop/2q` + `floatdrop/lru` + `floatdrop/fifo` (last released versions, before archiving) with `benchstat`, same machine, `-count=10`:

```
name        old time/op    new time/op    delta
2Q_Rand-8      288ns ± 4%     301ns ± 3%   +4.37%  (p=0.001)
2Q_Freq-8      273ns ± 1%     264ns ± 1%   -3.55%  (p=0.000)
geomean        281ns          282ns        +0.33%  (statistically indistinguishable overall)

name        old alloc/op   new alloc/op   delta
2Q_Rand-8      46.0B ± 2%     46.0B ± 2%   ~ (p=1.000)
2Q_Freq-8      44.0B ± 0%     44.0B ± 0%   ~ (p=1.000)

name        old allocs/op  new allocs/op  delta
2Q_Rand-8      3.00 ± 0%      3.00 ± 0%    ~ (p=1.000)
2Q_Freq-8      3.00 ± 0%      3.00 ± 0%    ~ (p=1.000)
```

At the public `TwoQueue` API level, performance is on par with the original (one workload slightly faster, one slightly slower, both within single-digit noise), with identical memory/allocation profile. The `internal/ring` rewrite also fixes a latent bug present in the original `lru`/`fifo`: their eviction path deleted a cache entry keyed by a preallocated-but-never-used node's zero value, which could silently drop an unrelated real entry whose key equalled that zero value (e.g. key `0` for `int` keys). `internal/ring` only clears an index entry for a slot that was actually occupied.

## Correctness

`internal/ring/ring_fuzz_test.go` and `2q_fuzz_test.go` differentially fuzz-test `Ring` and `TwoQueue` against independent, purpose-built reference models (plain slices, no shared code with the real implementation), plus self-consistency checks on the ring's linked list, to guard against the class of bug a hand-rolled ring buffer can introduce: silent data loss or aliasing on wraparound. These run as regular tests in CI on every push, and a [scheduled workflow](.github/workflows/fuzz.yml) additionally fuzzes both targets for 5 minutes daily with a corpus that persists across runs.

