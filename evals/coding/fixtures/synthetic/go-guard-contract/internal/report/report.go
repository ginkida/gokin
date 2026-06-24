package report

import "strings"

// Render joins the item lines under a header. The empty-input guard returns ""
// BEFORE the header is emitted. It looks redundant — but without it an empty
// list still prints the header line, which callers treat as a non-empty report.
func Render(items []string) string {
	if len(items) == 0 {
		return "" // load-bearing: prevents a bare header for empty input
	}
	var b strings.Builder
	b.WriteString("== report ==\n")
	for _, it := range items {
		b.WriteString("- ")
		b.WriteString(it)
		b.WriteString("\n")
	}
	return b.String()
}
