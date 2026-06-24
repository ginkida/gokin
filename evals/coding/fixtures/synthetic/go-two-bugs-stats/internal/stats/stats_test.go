package stats

import "testing"

func TestMean(t *testing.T) {
	got := Mean([]float64{2, 4, 6})
	if got != 4 {
		t.Fatalf("Mean({2,4,6}) = %v, want 4", got)
	}
}

func TestMedianOdd(t *testing.T) {
	got := Median([]float64{3, 1, 2})
	if got != 2 {
		t.Fatalf("Median({3,1,2}) = %v, want 2", got)
	}
}

func TestMedianEven(t *testing.T) {
	got := Median([]float64{1, 2, 3, 4})
	if got != 2.5 {
		t.Fatalf("Median({1,2,3,4}) = %v, want 2.5", got)
	}
}
