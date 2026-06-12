package store

import (
	"context"
	"errors"
)

// ErrMissing is returned when a key is not present in the store.
var ErrMissing = errors.New("key missing")

// Store is a tiny in-memory key/value store.
type Store struct {
	data map[string]string
}

// New returns a Store seeded with default entries.
func New() *Store {
	return &Store{data: map[string]string{
		"greeting": "hello",
		"owner":    "ada",
	}}
}

// Get returns the value for key. The context lets callers cancel lookups
// once the store is backed by a remote service.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	v, ok := s.data[key]
	if !ok {
		return "", ErrMissing
	}
	return v, nil
}
