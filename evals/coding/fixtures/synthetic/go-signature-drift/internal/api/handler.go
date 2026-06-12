package api

import "example.com/go-signature-drift/internal/store"

// Handler serves values out of the store.
type Handler struct {
	store *store.Store
}

// NewHandler wires a Handler to the given store.
func NewHandler(s *store.Store) *Handler {
	return &Handler{store: s}
}

// Greeting returns the stored greeting, or a fallback when missing.
func (h *Handler) Greeting() string {
	v, err := h.store.Get("greeting")
	if err != nil {
		return "hello, stranger"
	}
	return v
}
