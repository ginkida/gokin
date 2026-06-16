package dedup

// Dedup returns xs with duplicate strings removed, preserving the order of
// each value's FIRST occurrence. Callers render the result to users in this
// order, so the ordering is part of the contract: a "simpler" version that
// sorts (or otherwise reorders) the output is incorrect even though it also
// removes duplicates.
func Dedup(xs []string) []string {
	seen := make(map[string]struct{}, len(xs))
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}
