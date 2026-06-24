package cachestore

import "example.com/go-iface-drift/internal/store"

// Cache is an in-memory store.Store implementation used as a fast cache.
type Cache struct {
	m map[string]string
}

// New returns an empty Cache.
func New() *Cache {
	return &Cache{m: make(map[string]string)}
}

// Put stores a value.
func (s *Cache) Put(key, value string) error {
	s.m[key] = value
	return nil
}

// Get reads a value.
func (s *Cache) Get(key string) (string, bool) {
	v, ok := s.m[key]
	return v, ok
}

var _ store.Store = (*Cache)(nil)
