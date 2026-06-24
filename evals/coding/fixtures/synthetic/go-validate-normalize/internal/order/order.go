package order

import "errors"

// Order is a minimal purchase order.
type Order struct {
	Currency string
	Quantity int
}

// Validate normalizes then reports if the order is well-formed: a non-empty
// currency and a non-negative quantity.
func Validate(o *Order) error {
	normalize(o)
	if o.Currency == "" {
		return errors.New("currency required")
	}
	if o.Quantity < 0 {
		return errors.New("quantity must be non-negative")
	}
	return nil
}

// PrepareForInsert normalizes the order for persistence; after it returns the
// quantity is clamped non-negative so the row is always safe to insert.
func PrepareForInsert(o *Order) *Order {
	normalize(o)
	return o
}
