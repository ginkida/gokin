package service

import "example.com/go-iface-drift/internal/store"

// Writer saves values through a store.Store.
type Writer struct {
	s store.Store
}

// NewWriter wraps a store.Store.
func NewWriter(s store.Store) *Writer {
	return &Writer{s: s}
}

// Save persists key/value through the underlying store.
func (w *Writer) Save(key, value string) error {
	return w.s.Put(key, value)
}
