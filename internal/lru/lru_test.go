package lru

import (
	"sync"
	"testing"
)

func TestNew_RejectsNonPositiveSize(t *testing.T) {
	for _, size := range []int{0, -1} {
		if _, err := New[string, int](size); err == nil {
			t.Errorf("New(%d): want error, got nil", size)
		}
	}
	if _, err := New[string, int](1); err != nil {
		t.Errorf("New(1): unexpected error: %v", err)
	}
}

func TestGet_MissReturnsZeroValueAndFalse(t *testing.T) {
	c := mustNew[string, int](t, 2)
	if v, ok := c.Get("absent"); ok || v != 0 {
		t.Errorf("Get(absent) = (%d, %v), want (0, false)", v, ok)
	}
}

func TestAddThenGet_ReturnsStoredValue(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Add("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("Get(a) = (%d, %v), want (1, true)", v, ok)
	}
}

func TestAdd_UpdatesValueInPlaceWithoutGrowing(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Add("a", 1)
	if evicted := c.Add("a", 2); evicted {
		t.Error("Add(a, 2): updating an existing key must not evict")
	}
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Errorf("Get(a) after update = (%d, %v), want (2, true)", v, ok)
	}
	// A second distinct key must still fit: the update did not consume a slot.
	if evicted := c.Add("b", 3); evicted {
		t.Error("Add(b, 3): cache should still have a free slot after an update")
	}
}

func TestAdd_EvictsLeastRecentlyUsed(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Add("a", 1)
	c.Add("b", 2)
	// a is now least-recently-used; inserting c evicts it.
	if evicted := c.Add("c", 3); !evicted {
		t.Error("Add(c, 3): want eviction when over capacity")
	}
	if _, ok := c.Get("a"); ok {
		t.Error("a should have been evicted as least-recently-used")
	}
	for _, k := range []string{"b", "c"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%q should still be present", k)
		}
	}
}

func TestGet_PromotesEntryProtectingItFromEviction(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Add("a", 1)
	c.Add("b", 2)
	// Touch a so b becomes least-recently-used.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("precondition: a should be present")
	}
	c.Add("c", 3) // evicts b, not a
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted after a was promoted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a should have survived: it was the most-recently-used")
	}
}

func TestAdd_PromotesUpdatedEntry(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Add("a", 1)
	c.Add("b", 2)
	// Re-Add a: it must move to most-recently-used, so the next insert evicts b.
	c.Add("a", 10)
	c.Add("c", 3)
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted; re-Adding a should have promoted it")
	}
	if v, ok := c.Get("a"); !ok || v != 10 {
		t.Errorf("Get(a) = (%d, %v), want (10, true)", v, ok)
	}
}

func TestCache_ConcurrentAccessIsSafe(t *testing.T) {
	// Run under -race to catch unsynchronized access. A small cache plus many
	// goroutines guarantees constant contention and eviction churn.
	c := mustNew[int, int](t, 64)
	const goroutines, ops = 16, 2000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				k := (g*ops + i) % 128
				c.Add(k, k*10)
				if v, ok := c.Get(k); ok && v != k*10 {
					t.Errorf("Get(%d) = %d, want %d", k, v, k*10)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func mustNew[K comparable, V any](t *testing.T, size int) *Cache[K, V] {
	t.Helper()
	c, err := New[K, V](size)
	if err != nil {
		t.Fatalf("New(%d): unexpected error: %v", size, err)
	}
	return c
}

// Guard against accidental unbounded growth for a stress-ish sequential run.
func TestAdd_NeverExceedsCapacity(t *testing.T) {
	const size = 8
	c := mustNew[int, int](t, size)
	for i := 0; i < 1000; i++ {
		c.Add(i, i)
		if got := c.ll.Len(); got > size {
			t.Fatalf("after Add(%d): list length %d exceeds capacity %d", i, got, size)
		}
	}
	// Only the last `size` keys should remain.
	for i := 1000 - size; i < 1000; i++ {
		if _, ok := c.Get(i); !ok {
			t.Errorf("recent key %d should be present", i)
		}
	}
	if _, ok := c.Get(0); ok {
		t.Error("oldest key 0 should long since have been evicted")
	}
}
