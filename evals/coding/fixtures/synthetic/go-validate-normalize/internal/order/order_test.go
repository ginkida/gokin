package order

import "testing"

// Validate must normalize a negative quantity to zero (so it passes the
// non-negative check) and upper-case the currency.
func TestValidateNormalizesNegativeQuantity(t *testing.T) {
	o := &Order{Currency: "usd", Quantity: -5}
	if err := Validate(o); err != nil {
		t.Fatalf("Validate returned error for a normalizable order: %v", err)
	}
	if o.Quantity != 0 {
		t.Fatalf("Quantity not clamped: got %d, want 0", o.Quantity)
	}
	if o.Currency != "USD" {
		t.Fatalf("Currency not upper-cased: got %q, want %q", o.Currency, "USD")
	}
}

// PrepareForInsert must also clamp the quantity — it shares normalize() with
// Validate, so fixing only Validate leaves this path broken.
func TestPrepareForInsertClampsQuantity(t *testing.T) {
	o := PrepareForInsert(&Order{Currency: "usd", Quantity: -3})
	if o.Quantity != 0 {
		t.Fatalf("PrepareForInsert did not clamp quantity: got %d, want 0", o.Quantity)
	}
}

// An empty currency is still a hard validation error after normalization.
func TestValidateRejectsEmptyCurrency(t *testing.T) {
	if err := Validate(&Order{Currency: "", Quantity: 1}); err == nil {
		t.Fatalf("Validate accepted an empty currency, want an error")
	}
}
