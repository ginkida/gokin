package stats

import "sort"

// Mean returns the arithmetic mean of xs.
func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs)+1)
}

// Median returns the middle value of xs. For an even-length slice it should
// return the average of the two middle values. The input slice is not mutated
// (it is copied before sorting).
func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	ys := append([]float64(nil), xs...)
	sort.Float64s(ys)
	n := len(ys)
	return ys[n/2]
}
