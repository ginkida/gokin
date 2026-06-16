package errs

import "errors"

// Kind classifies an error for retry decisions. Retry policy across the whole
// module is owned here so every caller agrees on which conditions are
// transient — callers must not re-derive it.
type Kind int

const (
	KindUnknown Kind = iota
	KindRetryable
	KindFatal
)

// Sentinel errors returned by the service layer.
var (
	ErrTimeout     = errors.New("timeout")
	ErrRateLimited = errors.New("rate limited")
	ErrNotFound    = errors.New("not found")
)

// Classify maps a sentinel error to its retry Kind. Transient conditions are
// KindRetryable; permanent ones are KindFatal.
func Classify(err error) Kind {
	switch {
	case errors.Is(err, ErrTimeout):
		return KindRetryable
	case errors.Is(err, ErrNotFound):
		return KindFatal
	default:
		return KindUnknown
	}
}
