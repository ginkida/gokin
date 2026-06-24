package service

import (
	"errors"
	"testing"

	"example.com/go-iface-drift/internal/cachestore"
	"example.com/go-iface-drift/internal/store"
)

func TestWriter_Save_RejectsEmptyKey(t *testing.T) {
	w := NewWriter(cachestore.New())

	err := w.Save("", "v")
	if !errors.Is(err, store.ErrEmptyKey) {
		t.Fatalf("Save(\"\", \"v\") = %v, want store.ErrEmptyKey", err)
	}
}

func TestWriter_Save_StoresValidKey(t *testing.T) {
	w := NewWriter(cachestore.New())

	if err := w.Save("k", "v"); err != nil {
		t.Fatalf("Save(\"k\", \"v\") = %v, want nil", err)
	}
}
