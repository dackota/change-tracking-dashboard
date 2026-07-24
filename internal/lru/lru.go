// Package lru provides a fixed-capacity, least-recently-used cache that is
// safe for concurrent use. Its surface (New/Get/Add) mirrors the slice of
// github.com/hashicorp/golang-lru/v2 the diff engines relied on, so it is a
// drop-in replacement: a positive capacity is required, Get promotes an entry
// to most-recently-used and reports presence, and Add inserts or updates an
// entry and evicts the least-recently-used one once the cache is over
// capacity.
package lru

import (
	"container/list"
	"errors"
	"sync"
)

// entry is the key/value pair stored in each list element. The key is kept
// alongside the value so evicting the back element can delete its map entry
// without a reverse lookup.
type entry[K comparable, V any] struct {
	key   K
	value V
}

// Cache is a fixed-capacity LRU cache safe for concurrent use by multiple
// goroutines. A single mutex guards both the recency list and the index map,
// so Get (which reorders on a hit) and Add are mutually exclusive.
type Cache[K comparable, V any] struct {
	mu    sync.Mutex
	size  int
	ll    *list.List // front = most-recently-used, back = least
	items map[K]*list.Element
}

// New creates a Cache that holds at most size entries. size must be positive;
// a non-positive size returns an error rather than an unbounded or useless
// cache, matching the constructor contract the engines depend on.
func New[K comparable, V any](size int) (*Cache[K, V], error) {
	if size <= 0 {
		return nil, errors.New("lru: cache size must be positive")
	}
	return &Cache[K, V]{
		size:  size,
		ll:    list.New(),
		items: make(map[K]*list.Element, size),
	}, nil
}

// Get returns the value stored for key and whether it was present. A hit
// promotes the entry to most-recently-used.
func (c *Cache[K, V]) Get(key K) (value V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, hit := c.items[key]; hit {
		c.ll.MoveToFront(el)
		return el.Value.(*entry[K, V]).value, true
	}
	var zero V
	return zero, false
}

// Add inserts or updates the value for key, promoting it to
// most-recently-used, and evicts the least-recently-used entry if the cache
// is now over capacity. It reports whether an eviction occurred.
func (c *Cache[K, V]) Add(key K, value V) (evicted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, hit := c.items[key]; hit {
		c.ll.MoveToFront(el)
		el.Value.(*entry[K, V]).value = value
		return false
	}

	c.items[key] = c.ll.PushFront(&entry[K, V]{key: key, value: value})
	if c.ll.Len() > c.size {
		c.removeOldest()
		return true
	}
	return false
}

// removeOldest evicts the least-recently-used entry (the back of the list).
// The caller must hold c.mu.
func (c *Cache[K, V]) removeOldest() {
	el := c.ll.Back()
	if el == nil {
		return
	}
	c.ll.Remove(el)
	delete(c.items, el.Value.(*entry[K, V]).key)
}
