package billing

import "example.com/go-used-symbol-trap/internal/legacy"

// InvoiceRef builds the customer-facing invoice reference. Old accounts
// still require the legacy id format.
func InvoiceRef(region string, seq int, legacyAccount bool) string {
	if legacyAccount {
		return legacy.FormatLegacyID(region, seq)
	}
	return region + "/" + itoa(seq)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
