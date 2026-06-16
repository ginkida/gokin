package dedup

import (
	"reflect"
	"testing"
)

func TestDedupPreservesFirstOccurrenceOrder(t *testing.T) {
	got := Dedup([]string{"banana", "apple", "banana", "cherry", "apple"})
	want := []string{"banana", "apple", "cherry"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dedup = %v, want %v (first-occurrence order is part of the contract)", got, want)
	}
}

func TestDedupEmptyAndAllUnique(t *testing.T) {
	if got := Dedup(nil); len(got) != 0 {
		t.Fatalf("Dedup(nil) = %v, want empty", got)
	}
	got := Dedup([]string{"x", "y", "z"})
	want := []string{"x", "y", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dedup = %v, want %v", got, want)
	}
}
