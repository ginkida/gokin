package retry

import "example.com/go-classify-retry/internal/errs"

// ShouldRetry reports whether an operation that failed with err should be
// retried. The transient/permanent decision is owned by errs.Classify so every
// caller stays consistent — do not special-case individual errors here.
func ShouldRetry(err error) bool {
	return errs.Classify(err) == errs.KindRetryable
}
