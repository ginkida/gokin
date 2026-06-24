package memstore

import "example.com/go-iface-drift/internal/store"

// Mem is an in-memory store.Store implementation.
type Mem struct {
	m map[string]string
}

// New returns an empty Mem.
func New() *Mem {
	return &Mem{m: make(map[string]string)}
}

// Put honors the store.Store contract: it rejects an empty key.
func (s *Mem) Put(key, value string) error {
	if key == "" {
		return store.ErrEmptyKey
	}
	s.m[key] = value
	return nil
}

// Get reads a value.
func (s *Mem) Get(key string) (string, bool) {
	v, ok := s.m[key]
	return v, ok
}

var _ store.Store = (*Mem)(nil)
