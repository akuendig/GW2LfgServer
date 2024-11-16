package syncmap

import (
	"maps"
	"slices"
	"sync"
)

// Map is a generic concurrent safe map
type Map[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

// New creates and initializes a new Map
func New[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		m: make(map[K]V),
	}
}

// Get retrieves a value for a key
func (m *Map[K, V]) Get(key K) (V, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.m[key]
	return val, ok
}

// Set stores a value for a key
func (m *Map[K, V]) Set(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[key] = value
}

// Updates updates a value for a key
func (m *Map[K, V]) Update(key K, update func(V, bool) (V, bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.m[key]
	newVal, newOk := update(val, ok)
	if newOk {
		m.m[key] = newVal
	} else {
		delete(m.m, key)
	}
}

// Delete removes a key and its value
func (m *Map[K, V]) Delete(key K) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.m[key]
	delete(m.m, key)
	return val, ok
}

// Range calls f sequentially for each key and value in the map
func (m *Map[K, V]) Snapshot() []V {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return slices.Collect(maps.Values(m.m))
}
