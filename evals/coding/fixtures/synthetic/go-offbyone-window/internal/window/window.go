package window

// LastN returns the last n items of the slice. If n is zero or negative
// it returns nil; if n exceeds the slice length it returns the whole slice.
func LastN(items []string, n int) []string {
	if n <= 0 {
		return nil
	}
	if n > len(items) {
		n = len(items)
	}
	return items[len(items)-n+1:]
}
