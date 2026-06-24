package store

import "errors"

// ErrEmptyKey is returned by Put when the key is empty.
var ErrEmptyKey = errors.New("store: empty key")

// Store persists string values keyed by string.
//
// CONTRACT: Put MUST reject an empty key by returning ErrEmptyKey without
// storing the value. Every implementation is required to honor this.
type Store interface {
	Put(key, value string) error
	Get(key string) (string, bool)
}
