package app

import (
	"fmt"
	"time"

	"gokin/internal/client"
	"gokin/internal/ui"
)

// appStatusCallback implements client.StatusCallback to send status updates to the UI.
type appStatusCallback struct {
	app *App
}

// OnRetry is called when the client is retrying a failed request.
func (c *appStatusCallback) OnRetry(attempt, maxAttempts int, delay time.Duration, reason string) {
	if c.app == nil || c.app.program == nil {
		return
	}

	msg := fmt.Sprintf("Retry %d/%d in %s (%s)",
		attempt, maxAttempts, delay.Round(time.Second), reason)

	c.app.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusRetry,
		Message: msg,
		Details: map[string]any{
			"attempt":     attempt,
			"maxAttempts": maxAttempts,
			"delay":       delay,
			"reason":      reason,
		},
	})
}

// OnRateLimit is called when the client is waiting due to rate limiting.
func (c *appStatusCallback) OnRateLimit(waitTime time.Duration) {
	if c.app == nil || c.app.program == nil {
		return
	}

	msg := fmt.Sprintf("Rate limit, waiting %s...", waitTime.Round(time.Second))

	c.app.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusRateLimit,
		Message: msg,
		Details: map[string]any{
			"waitTime": waitTime,
		},
	})
}

// OnStreamIdle is called when the streaming response has been idle for a while.
func (c *appStatusCallback) OnStreamIdle(elapsed time.Duration) {
	if c.app == nil || c.app.program == nil {
		return
	}

	msg := fmt.Sprintf("Waiting for response %s...", elapsed.Round(time.Second))
	if elapsed >= 20*time.Second {
		msg = fmt.Sprintf("Waiting for response %s... (ESC to cancel)", elapsed.Round(time.Second))
	}

	c.app.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusStreamIdle,
		Message: msg,
		Details: map[string]any{
			"elapsed": elapsed,
		},
	})
}

// OnThinkingIdle is called when a thinking-enabled model is in its silent reasoning phase.
func (c *appStatusCallback) OnThinkingIdle(elapsed time.Duration, provider string) {
	if c.app == nil || c.app.program == nil {
		return
	}

	msg := fmt.Sprintf("%s is thinking %s...", provider, elapsed.Round(time.Second))
	if elapsed >= 60*time.Second {
		msg = fmt.Sprintf("%s is thinking %s... (ESC to cancel)", provider, elapsed.Round(time.Second))
	}

	c.app.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusThinkingIdle,
		Message: msg,
		Details: map[string]any{
			"elapsed":  elapsed,
			"provider": provider,
		},
	})
}

// OnStreamResume is called when the stream resumes after being idle.
func (c *appStatusCallback) OnStreamResume() {
	if c.app == nil || c.app.program == nil {
		return
	}

	// Send resume message - this can be used to clear any warning toasts
	c.app.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusStreamResume,
		Message: "",
	})
}

// OnError is called when an error occurs.
func (c *appStatusCallback) OnError(err error, recoverable bool) {
	if c.app == nil || c.app.program == nil {
		return
	}

	msg := err.Error()
	if recoverable {
		msg = "Recoverable error: " + msg
	}
	ft := client.DetectFailureTelemetry(err)

	c.app.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusRecoverableError,
		Message: msg,
		Details: map[string]any{
			"recoverable": recoverable,
			"error":       err.Error(),
			"reason":      ft.Reason,
			"partial":     ft.Partial,
			"timeout":     ft.Timeout,
			"provider":    ft.Provider,
		},
	})
}

// attachStatusCallback binds status callbacks to any client that supports it.
func attachStatusCallback(c client.Client, cb client.StatusCallback) {
	if c == nil || cb == nil {
		return
	}
	if setter, ok := c.(interface{ SetStatusCallback(client.StatusCallback) }); ok {
		setter.SetStatusCallback(cb)
	}
}
