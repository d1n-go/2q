package twoqueue

import (
	"sync"
	"testing"
)

// TestSet_ReturnContract pins the documented meaning of Set's result in
// every branch: a non-nil result is always a key/value pair the cache no
// longer holds.
func TestSet_ReturnContract(t *testing.T) {
	l := NewParams[string, int](2, 2, 2)

	if e := l.Set("a", 1); e != nil {
		t.Fatalf("new key with room in recent: got %+v, want nil", e)
	}
	if e := l.Set("a", 2); e == nil || e.Key != "a" || e.Value != 1 {
		t.Fatalf("update in recent must return the previous value {a 1}, got %+v", e)
	}
	if v := l.Peek("a"); v == nil || *v != 2 || l.frequent.Contains("a") {
		t.Fatalf("update in recent must keep the key in recent with the new value, got %v", v)
	}

	l.Set("b", 1)
	if e := l.Set("c", 1); e == nil || e.Key != "a" || e.Value != 2 {
		t.Fatalf("a full recent must evict its oldest entry {a 2}, got %+v", e)
	}
	if l.Contains("a") {
		t.Fatal("an entry reported as evicted must not be cached")
	}

	if e := l.Set("a", 3); e != nil {
		t.Fatalf("promotion into a frequent queue with room: got %+v, want nil", e)
	}
	if e := l.Set("a", 4); e == nil || e.Key != "a" || e.Value != 3 {
		t.Fatalf("update in frequent must return the previous value {a 3}, got %+v", e)
	}
	if v := l.Get("a"); v == nil || *v != 4 {
		t.Fatalf("update in frequent must store the new value, got %v", v)
	}

	// Fill frequent, then promote one more key: frequent evicts its LRU entry.
	l.Set("d", 1) // b -> ghost
	l.Set("b", 1) // b -> frequent [b a]
	l.Set("e", 1) // c -> ghost
	if e := l.Set("c", 5); e == nil || e.Key != "a" || e.Value != 4 {
		t.Fatalf("promotion into a full frequent must evict its LRU entry {a 4}, got %+v", e)
	}
	if l.Contains("a") {
		t.Fatal("an entry reported as evicted must not be cached")
	}
}

// TestGet_PointerStaysValid pins the guarantee behind Get returning a
// pointer: the pointee is never overwritten in place, neither by a later
// update of the same key nor by its slot being reused for another key.
// Run under -race this also shows reading it needs no lock.
func TestGet_PointerStaysValid(t *testing.T) {
	l := NewParams[int, [4]int](2, 2, 2)
	l.Set(1, [4]int{1, 1, 1, 1})
	p := l.Get(1)

	l.Set(1, [4]int{2, 2, 2, 2}) // update in place
	for k := 10; k < 20; k++ {   // churn: the slot of 1 gets reused
		l.Set(k, [4]int{k, k, k, k})
	}
	if *p != [4]int{1, 1, 1, 1} {
		t.Fatalf("pointer from Get was overwritten: %v", *p)
	}

	l = New[int, [4]int](64)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 5000 {
				if p := l.Get(i % 64); p != nil {
					if v := *p; v[0] != v[3] {
						t.Errorf("torn read: %v", v)
						return
					}
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := range 5000 {
				l.Set(i%128, [4]int{i, i, i, i})
			}
		}()
	}
	wg.Wait()
}
