package poll

import (
	"context"
	"errors"
	"time"
)

// ErrNotReady is returned when the probe never reported ready within the
// polling budget.
var ErrNotReady = errors.New("poll: probe never became ready")

const (
	maxAttempts  = 50
	pollInterval = 100 * time.Millisecond
)

// WaitReady polls probe until it returns true. It must respect ctx
// cancellation and return promptly with the context's error.
func WaitReady(ctx context.Context, probe func() bool) error {
	for i := 0; i < maxAttempts; i++ {
		if probe() {
			return nil
		}
		time.Sleep(pollInterval)
	}
	return ErrNotReady
}
