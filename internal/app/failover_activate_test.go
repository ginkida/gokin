package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"
)

// stubNewClientForFailover replaces newClientForFailover for the duration of
// the test. The returned function restores the original; defer it.
func stubNewClientForFailover(t *testing.T, fn func(ctx context.Context, cfg *config.Config, modelID string) (client.Client, error)) func() {
	t.Helper()
	prev := newClientForFailover
	newClientForFailover = fn
	return func() { newClientForFailover = prev }
}

func TestActivateEmergencyFailover_NoFallbackReturnsError(t *testing.T) {
	app := &App{
		ctx: context.Background(),
		config: &config.Config{
			Model: config.ModelConfig{Provider: "glm", Name: "glm-5.1"},
			API:   config.APIConfig{GLMKey: "x"}, // only one provider with creds
		},
	}
	restore := stubNewClientForFailover(t, func(context.Context, *config.Config, string) (client.Client, error) {
		t.Error("factory should NOT be called when no fallback is available")
		return nil, nil
	})
	defer restore()

	_, err := app.activateEmergencyFailoverClient()
	if err == nil {
		t.Fatal("expected error when no fallback candidates")
	}
	if !strings.Contains(err.Error(), "no alternative providers") {
		t.Errorf("err = %v, want 'no alternative providers'", err)
	}
}

func TestActivateEmergencyFailover_NilAppOrConfigReturnsError(t *testing.T) {
	// nil config → error
	app := &App{}
	_, err := app.activateEmergencyFailoverClient()
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestActivateEmergencyFailover_SwapsClientAndReturnsSummary(t *testing.T) {
	oldMock := testkit.NewMockClient()
	oldMock.SetModel("old-model")

	newMock := testkit.NewMockClient()
	newMock.SetModel("new-model")

	app := &App{
		ctx:    context.Background(),
		client: oldMock,
		config: &config.Config{
			Model: config.ModelConfig{
				Provider:          "glm",
				Name:              "glm-5.1",
				FallbackProviders: []string{"kimi"},
			},
			API: config.APIConfig{
				GLMKey:  "x",
				KimiKey: "y",
			},
		},
	}

	var factoryCalls atomic.Int32
	var capturedModel string
	var capturedProvider string
	restore := stubNewClientForFailover(t, func(ctx context.Context, cfg *config.Config, modelID string) (client.Client, error) {
		factoryCalls.Add(1)
		capturedModel = modelID
		capturedProvider = cfg.Model.Provider
		return newMock, nil
	})
	defer restore()

	summary, err := app.activateEmergencyFailoverClient()
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if factoryCalls.Load() != 1 {
		t.Errorf("factory called %d times, want 1", factoryCalls.Load())
	}

	// The first candidate becomes the new primary provider.
	if capturedProvider != "glm" {
		t.Errorf("factory called with provider %q, want glm", capturedProvider)
	}
	if capturedModel != "glm-5.1" {
		t.Errorf("factory called with model %q, want glm-5.1", capturedModel)
	}

	// Summary describes the fallback chain.
	if !strings.Contains(summary, "glm") || !strings.Contains(summary, "kimi") {
		t.Errorf("summary = %q, expected both providers in chain", summary)
	}
	if !strings.Contains(summary, "->") {
		t.Errorf("summary = %q, expected '->' separator", summary)
	}

	// Client was swapped.
	app.mu.Lock()
	got := app.client
	app.mu.Unlock()
	if got != newMock {
		t.Error("app.client was not swapped to the new mock client")
	}

	// Config.Model.Provider was updated to match the candidate chain head.
	if app.config.Model.Provider != "glm" {
		t.Errorf("config provider = %q, want glm", app.config.Model.Provider)
	}
	if app.config.API.ActiveProvider != "glm" {
		t.Errorf("active provider = %q, want glm", app.config.API.ActiveProvider)
	}
	if app.config.API.Backend != "glm" {
		t.Errorf("backend = %q, want glm", app.config.API.Backend)
	}
}

func TestActivateEmergencyFailover_PropagatesSystemInstructionAndThinking(t *testing.T) {
	newMock := testkit.NewMockClient()
	session := chat.NewSession()
	session.SystemInstruction = "be concise"

	app := &App{
		ctx:     context.Background(),
		session: session,
		config: &config.Config{
			Model: config.ModelConfig{
				Provider:          "glm",
				Name:              "glm-5.1",
				FallbackProviders: []string{"kimi"},
				ThinkingBudget:    2048,
			},
			API: config.APIConfig{GLMKey: "x", KimiKey: "y"},
		},
	}

	restore := stubNewClientForFailover(t, func(context.Context, *config.Config, string) (client.Client, error) {
		return newMock, nil
	})
	defer restore()

	if _, err := app.activateEmergencyFailoverClient(); err != nil {
		t.Fatalf("activate: %v", err)
	}

	if got := newMock.SystemInstruction(); got != "be concise" {
		t.Errorf("SystemInstruction on new client = %q, want 'be concise'", got)
	}
	if got := newMock.ThinkingBudget(); got != 2048 {
		t.Errorf("ThinkingBudget on new client = %d, want 2048", got)
	}
}

func TestActivateEmergencyFailover_ClosesOldClientAsync(t *testing.T) {
	// oldMock tracks Close via a sentinel channel. Close is called in a
	// goroutine, so we wait for it.
	oldClosed := make(chan struct{}, 1)
	oldMock := &closeTrackingMock{MockClient: testkit.NewMockClient(), closed: oldClosed}

	newMock := testkit.NewMockClient()

	app := &App{
		ctx:    context.Background(),
		client: oldMock,
		config: &config.Config{
			Model: config.ModelConfig{
				Provider:          "glm",
				FallbackProviders: []string{"kimi"},
			},
			API: config.APIConfig{GLMKey: "x", KimiKey: "y"},
		},
	}

	restore := stubNewClientForFailover(t, func(context.Context, *config.Config, string) (client.Client, error) {
		return newMock, nil
	})
	defer restore()

	if _, err := app.activateEmergencyFailoverClient(); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// Wait up to 1s for the async Close.
	select {
	case <-oldClosed:
	case <-time.After(time.Second):
		t.Error("old client Close() was never called")
	}
}

func TestActivateEmergencyFailover_ClientFactoryErrorPropagates(t *testing.T) {
	app := &App{
		ctx: context.Background(),
		config: &config.Config{
			Model: config.ModelConfig{
				Provider:          "glm",
				FallbackProviders: []string{"kimi"},
			},
			API: config.APIConfig{GLMKey: "x", KimiKey: "y"},
		},
	}

	factoryErr := errors.New("auth refused")
	restore := stubNewClientForFailover(t, func(context.Context, *config.Config, string) (client.Client, error) {
		return nil, factoryErr
	})
	defer restore()

	_, err := app.activateEmergencyFailoverClient()
	if !errors.Is(err, factoryErr) {
		t.Errorf("err = %v, want wrap of %v", err, factoryErr)
	}

	// Client was NOT swapped — still nil.
	app.mu.Lock()
	got := app.client
	app.mu.Unlock()
	if got != nil {
		t.Errorf("client was swapped despite factory failure: %v", got)
	}
}

// closeTrackingMock wraps a MockClient to notify on Close. The "async
// close" path in activate wraps Close in go func() { _ = c.Close() }().
type closeTrackingMock struct {
	*testkit.MockClient
	closed   chan struct{}
	closedMu sync.Mutex
	done     bool
}

func (c *closeTrackingMock) Close() error {
	c.closedMu.Lock()
	already := c.done
	c.done = true
	c.closedMu.Unlock()
	if !already {
		select {
		case c.closed <- struct{}{}:
		default:
		}
	}
	return c.MockClient.Close()
}
