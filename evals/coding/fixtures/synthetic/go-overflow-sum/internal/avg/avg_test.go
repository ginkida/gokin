package avg

import "testing"

func TestAverageSmall(t *testing.T) {
	if got := Average([]int32{2, 4, 6}); got != 4 {
		t.Fatalf("Average([2,4,6]) = %d, want 4", got)
	}
}

func TestAverageNoOverflow(t *testing.T) {
	// Three values of 2e9 each. The true mean is 2e9, which fits in an int32
	// (max ~2.147e9). But the running sum (6e9) does NOT fit in int32, so an
	// int32 accumulator overflows and the result wraps to a wrong value.
	xs := []int32{2000000000, 2000000000, 2000000000}
	if got := Average(xs); got != 2000000000 {
		t.Fatalf("Average(%v) = %d, want 2000000000", xs, got)
	}
}
