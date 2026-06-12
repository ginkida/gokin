package metrics

// Counter tracks per-label event counts. It is documented as safe for
// concurrent use by multiple goroutines.
type Counter struct {
	counts map[string]int
}

// NewCounter returns an empty counter ready for concurrent use.
func NewCounter() *Counter {
	return &Counter{counts: make(map[string]int)}
}

// Inc increments the count for the given label.
func (c *Counter) Inc(label string) {
	c.counts[label]++
}

// Get returns the current count for the given label.
func (c *Counter) Get(label string) int {
	return c.counts[label]
}
