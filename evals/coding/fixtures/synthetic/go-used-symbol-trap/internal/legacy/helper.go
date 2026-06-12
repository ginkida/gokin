package legacy

import "fmt"

// FormatLegacyID renders the pre-2024 invoice id format.
//
// Deprecated: candidate for removal once all callers migrate to the new
// id scheme.
func FormatLegacyID(region string, seq int) string {
	return fmt.Sprintf("%s-%06d", region, seq)
}
