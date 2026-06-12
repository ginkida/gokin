package report

// Keys returns the labels present in the totals map in deterministic
// (sorted) order, so reports render identically run to run.
func Keys(totals map[string]int) []string {
	out := make([]string, 0, len(totals))
	for k := range totals {
		out = append(out, k)
	}
	return out
}
