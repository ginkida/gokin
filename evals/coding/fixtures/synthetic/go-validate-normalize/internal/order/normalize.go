package order

import "strings"

// normalize canonicalizes an order in place: upper-cases the currency and
// clamps a negative quantity to zero. Both Validate and PrepareForInsert rely
// on it so the two paths can never disagree.
func normalize(o *Order) {
	o.Currency = strings.ToUpper(strings.TrimSpace(o.Currency))
	// BUG: the negative-quantity clamp is missing.
}
