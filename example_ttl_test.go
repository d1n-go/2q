package twoqueue_test

import (
	"fmt"
	"testing"
	"time"

	twoqueue "github.com/d1n-go/2q"
)

// Expiring pairs a value with the moment it stops being valid. Expiry is
// lazy: nothing runs when the TTL passes, Valid just stops returning the
// value. The entry, value included, stays cached until it is evicted,
// replaced or removed, so expired values take no more memory than the
// cache's capacity already allows.
type Expiring[T any] struct {
	value    T
	deadline time.Time
}

func WithTTL[T any](value T, ttl time.Duration) Expiring[T] {
	return Expiring[T]{value: value, deadline: time.Now().Add(ttl)}
}

// Valid returns the value, or nil once the TTL has passed. It is nil-safe
// so it can be chained directly onto a cache Get, and the zero Expiring
// counts as expired.
func (e *Expiring[T]) Valid() *T {
	return e.ValidAt(time.Now())
}

// ValidAt is Valid as of the given time.
func (e *Expiring[T]) ValidAt(now time.Time) *T {
	if e == nil || !now.Before(e.deadline) {
		return nil
	}
	return &e.value
}

func ExampleTwoQueue_ttl() {
	cache := twoqueue.New[string, Expiring[string]](256)

	cache.Set("Hello", WithTTL("Bye", time.Hour))

	if v := cache.Get("Hello").Valid(); v != nil {
		fmt.Println(*v)
	}
	// Output: Bye
}

// TestExpiring guards the README example: a cached value must stop being
// returned exactly at its deadline, without depending on scheduling.
func TestExpiring(t *testing.T) {
	cache := twoqueue.New[string, Expiring[string]](256)
	cache.Set("k", WithTTL("v", time.Minute))

	e := cache.Get("k")
	if e == nil {
		t.Fatal("entry must be cached")
	}
	if v := e.ValidAt(e.deadline.Add(-time.Nanosecond)); v == nil || *v != "v" {
		t.Fatalf("value must be readable before the deadline, got %v", v)
	}
	if v := e.ValidAt(e.deadline); v != nil {
		t.Fatalf("value must expire at the deadline, got %q", *v)
	}
	if v := e.ValidAt(e.deadline.Add(time.Hour)); v != nil {
		t.Fatalf("value must stay expired after the deadline, got %q", *v)
	}

	cache.Set("gone", WithTTL("v", 0))
	if v := cache.Get("gone").Valid(); v != nil {
		t.Fatalf("a zero TTL must expire immediately, got %q", *v)
	}
	if v := cache.Get("missing").Valid(); v != nil {
		t.Fatalf("Valid on a missing entry must be nil, got %q", *v)
	}
	if v := new(Expiring[string]).Valid(); v != nil {
		t.Fatalf("the zero Expiring must count as expired, got %q", *v)
	}
}
