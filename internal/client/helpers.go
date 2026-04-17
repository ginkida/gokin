package client

// Ptr returns a pointer to the given value. Useful for building optional
// fields in request structs without needing an intermediate variable.
func Ptr[T any](v T) *T {
	return &v
}
