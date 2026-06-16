package paging

// offset returns the 0-based index of the first item on the given 1-based
// page. Page 1 starts at index 0, page 2 at index size, and so on.
func offset(page, size int) int {
	if page < 1 {
		page = 1
	}
	return page * size
}
