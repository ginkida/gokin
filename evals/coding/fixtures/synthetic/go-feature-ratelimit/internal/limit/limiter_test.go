package limit

import "testing"

func TestAllowExhaustsBucket(t *testing.T) {
	l := NewLimiter(3)
	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("Allow() #%d = false, want true while tokens remain", i+1)
		}
	}
	if l.Allow() {
		t.Fatal("Allow() #4 = true, want false on empty bucket")
	}
}

func TestRefillCapsAtCapacity(t *testing.T) {
	l := NewLimiter(2)
	l.Allow()
	l.Allow()
	l.Refill(5)
	if !l.Allow() || !l.Allow() {
		t.Fatal("expected 2 tokens after Refill(5) on capacity-2 bucket")
	}
	if l.Allow() {
		t.Fatal("Refill must cap at capacity; third Allow() must be false")
	}
}
