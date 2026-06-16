package paging

// Page returns the items belonging to a 1-based page of the given size.
// Page numbers below 1 are treated as page 1; a page past the end returns an
// empty slice. The result is rendered directly to users, so the first page
// must start at the first item.
//
// The slice math here is correct — the start index it relies on comes from
// offset().
func Page(items []int, page, size int) []int {
	if size <= 0 {
		return []int{}
	}
	start := offset(page, size)
	if start >= len(items) {
		return []int{}
	}
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}
