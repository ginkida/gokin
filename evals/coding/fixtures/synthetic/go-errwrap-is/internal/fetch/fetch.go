package fetch

import (
	"errors"
	"fmt"
)

// ErrNotFound is the sentinel for missing documents. Callers are expected
// to detect it with errors.Is.
var ErrNotFound = errors.New("document not found")

// Lookup returns the document body for id, or an error that wraps
// ErrNotFound when the id is unknown.
func Lookup(docs map[string]string, id string) (string, error) {
	body, ok := docs[id]
	if !ok {
		return "", fmt.Errorf("lookup %q: %v", id, ErrNotFound)
	}
	return body, nil
}
