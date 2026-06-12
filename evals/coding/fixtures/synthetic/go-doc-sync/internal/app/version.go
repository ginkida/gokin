package app

// MaxRetries is how many times a failed request is retried before giving up.
// Documented in README.md — keep the two in sync.
const MaxRetries = 8

// Retries returns the effective retry budget.
func Retries() int {
	return MaxRetries
}
