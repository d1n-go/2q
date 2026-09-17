package twoqueue_test

import (
	"fmt"
	"sync/atomic"
	"testing"
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

func ExampleTwoQueue_ttl() {
	cache := twoqueue.New[string, Expiring[string]](256)

	cache.Set("Hello", WithTTL("Bye", time.Hour))

	if v := cache.Get("Hello").Valid(); v != nil {
		fmt.Println(*v)
	}
	// Output: Bye
}

// TestExpiringReleasesAfterTTL guards the README example: the value
// stored in the cache must actually go away when the TTL passes.
func TestExpiringReleasesAfterTTL(t *testing.T) {
	cache := twoqueue.New[string, Expiring[string]](256)
	cache.Set("k", WithTTL("v", 20*time.Millisecond))

	if v := cache.Get("k").Valid(); v == nil || *v != "v" {
		t.Fatalf("value must be readable before the TTL, got %v", v)
	}

	deadline := time.Now().Add(2 * time.Second)
	for cache.Get("k").Valid() != nil {
		if time.Now().After(deadline) {
			t.Fatal("cached value is still valid long after its TTL")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if cache.Get("k") == nil {
		t.Fatal("the entry itself should remain until evicted; only its value is released")
	}
}
