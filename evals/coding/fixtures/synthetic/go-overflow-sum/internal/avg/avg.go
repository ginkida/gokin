package avg

// Average returns the integer mean of xs. BUG: the running sum is int32 and
// overflows when the values are large, yielding a wrong (often negative) mean.
func Average(xs []int32) int32 {
	if len(xs) == 0 {
		return 0
	}
	var sum int32
	for _, x := range xs {
		sum += x
	}
	return sum / int32(len(xs))
}
