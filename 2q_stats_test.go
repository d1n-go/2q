package twoqueue

import (
	"math"
	"testing"
)

func TestStats(t *testing.T) {
	l := NewParams[int, int](2, 2, 2)

	if s := l.Stats(); s != (Stats{}) || s.HitRate() != 0 {
		t.Fatalf("fresh cache: %+v rate=%v", s, s.HitRate())
	}

	l.Get(1) // miss on an empty cache
	l.Set(1, 1)
	l.Get(1) // hit in recent
	l.Set(2, 2)
	l.Set(3, 3) // 1 -> ghost
	l.Get(1)    // ghost only: a miss
	l.Set(1, 1) // promoted
	l.Get(1)    // hit in frequent

	// None of these are lookups on behalf of a consumer.
	l.Peek(1)
	l.Contains(1)
	l.Contains(42)
	l.Keys()
	l.Len()

	want := Stats{Hits: 2, Misses: 2}
	if s := l.Stats(); s != want {
		t.Fatalf("Stats=%+v want %+v", s, want)
	}
	if r := l.Stats().HitRate(); r != 0.5 {
		t.Fatalf("HitRate=%v want 0.5", r)
	}

	// Purge keeps the counters, ResetStats hands them back and zeroes them.
	l.Purge()
	if s := l.Stats(); s != want {
		t.Fatalf("Purge must not reset stats: %+v", s)
	}
	if s := l.ResetStats(); s != want {
		t.Fatalf("ResetStats returned %+v want %+v", s, want)
	}
	if s := l.Stats(); s != (Stats{}) {
		t.Fatalf("after ResetStats: %+v", s)
	}

	// The snapshot is a copy.
	l.Get(7)
	s := l.Stats()
	s.Misses = 100
	if l.Stats().Misses != 1 {
		t.Fatal("modifying a Stats snapshot must not affect the cache")
	}
}

func TestStatsHitRate(t *testing.T) {
	const m = math.MaxUint64
	for _, tc := range []struct {
		s    Stats
		want float64
	}{
		{Stats{}, 0},
		{Stats{Hits: 1}, 1},
		{Stats{Misses: 1}, 0},
		{Stats{Hits: 3, Misses: 1}, 0.75},
		{Stats{Hits: m}, 1},
		{Stats{Misses: m}, 0},
		// Hits+Misses overflows uint64 in all of these.
		{Stats{Hits: m, Misses: 1}, 1},
		{Stats{Hits: m, Misses: 2}, 1},
		{Stats{Hits: 1, Misses: m}, math.Ldexp(1, -64)},
		{Stats{Hits: m, Misses: m}, 0.5},
	} {
		got := tc.s.HitRate()
		if got != tc.want || got < 0 || got > 1 {
			t.Errorf("%+v.HitRate() = %v, want %v", tc.s, got, tc.want)
		}
	}
}
