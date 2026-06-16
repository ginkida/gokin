package retry

import (
	"testing"

	"example.com/go-classify-retry/internal/errs"
)

func TestShouldRetry(t *testing.T) {
	if !ShouldRetry(errs.ErrTimeout) {
		t.Fatal("timeouts are transient and must be retried")
	}
	if !ShouldRetry(errs.ErrRateLimited) {
		t.Fatal("rate-limit errors are transient and must be retried")
	}
	if ShouldRetry(errs.ErrNotFound) {
		t.Fatal("not-found is permanent and must not be retried")
	}
}
