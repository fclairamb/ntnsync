package store

import (
	"container/list"
	"sync"
)

// lruCache is a small, bounded, concurrency-safe least-recently-used cache.
//
// The GitHub backend keys its caches by git object SHA, which is
// content-addressed: an entry can never become stale, only evicted.
type lruCache[V any] struct {
	mu       sync.Mutex
	capacity int
	order    *list.List
	items    map[string]*list.Element
}

// lruEntry is one cached key/value pair.
type lruEntry[V any] struct {
	key   string
	value V
}

// newLRUCache creates a cache holding at most capacity entries.
func newLRUCache[V any](capacity int) *lruCache[V] {
	if capacity < 1 {
		capacity = 1
	}
	return &lruCache[V]{
		capacity: capacity,
		order:    list.New(),
		items:    make(map[string]*list.Element, capacity),
	}
}

// Get returns the cached value for key, if present.
func (c *lruCache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}

	c.order.MoveToFront(elem)

	entry, ok := elem.Value.(*lruEntry[V])
	if !ok {
		var zero V
		return zero, false
	}

	return entry.value, true
}

// Put stores a value, evicting the least recently used entry when full.
func (c *lruCache[V]) Put(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		if entry, isEntry := elem.Value.(*lruEntry[V]); isEntry {
			entry.value = value
		}
		return
	}

	c.items[key] = c.order.PushFront(&lruEntry[V]{key: key, value: value})

	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.order.Remove(oldest)
		if entry, isEntry := oldest.Value.(*lruEntry[V]); isEntry {
			delete(c.items, entry.key)
		}
	}
}

// Len returns the number of cached entries.
func (c *lruCache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
