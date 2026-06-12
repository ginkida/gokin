package billing

import "testing"

func TestInvoiceRefLegacyAccount(t *testing.T) {
	if got := InvoiceRef("EU", 42, true); got != "EU-000042" {
		t.Fatalf("InvoiceRef(legacy) = %q, want EU-000042", got)
	}
}

func TestInvoiceRefModernAccount(t *testing.T) {
	if got := InvoiceRef("EU", 42, false); got != "EU/42" {
		t.Fatalf("InvoiceRef(modern) = %q, want EU/42", got)
	}
}
