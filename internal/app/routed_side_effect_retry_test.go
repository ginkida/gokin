package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/router"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type routedUnsafeRunner struct {
	spawnCalls atomic.Int32
	cause      error
	result     *agent.AgentResult
}

func (r *routedUnsafeRunner) Spawn(
	context.Context, string, string, int, string,
) (string, error) {
	r.spawnCalls.Add(1)
	return "stateful-agent", r.cause
}

func (r *routedUnsafeRunner) SpawnAsync(
	context.Context, string, string, int, string,
) string {
	r.spawnCalls.Add(1)
	return "stateful-agent"
}

func (r *routedUnsafeRunner) GetResult(string) (*agent.AgentResult, bool) {
	return r.result, true
}

// TestProcessMessage_RoutedStatefulFailureNeverRetriesOrSchedulesRecovery pins
// the cross-layer exactly-once boundary: a routed sub-agent can run bash/edit,
// then fail on its next model round with an otherwise-retryable error. The App
// must not replay the top-level prompt with a fresh agent, compact-and-retry, or
// publish a durable/background recovery that has no sub-agent checkpoint ledger.
func TestProcessMessage_RoutedStatefulFailureNeverRetriesOrSchedulesRecovery(t *testing.T) {
	testCases := []struct {
		name  string
		cause error
	}{
		{
			name: "rate_limit",
			cause: &client.HTTPError{
				StatusCode: 429,
				Message:    "rate limited after stateful tool result",
			},
		},
		{
			name:  "model_round_timeout",
			cause: client.ErrModelRoundTimeout,
		},
		{
			name: "context_too_long",
			cause: &client.HTTPError{
				StatusCode: 400,
				Message:    "context length exceeded after stateful tool result",
			},
		},
	}

	const prompt = "Refactor authentication across packages. Analyze every caller and update interfaces. Add migration tests and optimize error handling for all providers."

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Model.Provider = "mock"
			cfg.Model.Name = "mock-model"
			cfg.DoneGate.Enabled = false

			mock := testkit.NewMockClient()
			registry := tools.NewRegistry()
			executor := tools.NewExecutor(registry, mock, time.Second)
			runner := &routedUnsafeRunner{
				cause: tc.cause,
				result: &agent.AgentResult{
					AgentID:              "stateful-agent",
					Status:               agent.AgentStatusFailed,
					Completed:            true,
					Error:                tc.cause.Error(),
					StatefulToolAttempts: 1,
					TouchedPaths:         []string{"internal/auth/store.go"},
				},
			}
			taskRouter := router.NewRouter(
				&router.RouterConfig{
					Enabled:            true,
					DecomposeThreshold: 100,
					ParallelThreshold:  100,
				},
				executor, runner, mock, registry, false, t.TempDir(),
			)
			decision := taskRouter.Route(prompt)
			if decision.Handler != router.HandlerSubAgent || decision.Background {
				t.Fatalf("test prompt route = handler:%v background:%v analysis:%+v; want foreground sub-agent",
					decision.Handler, decision.Background, decision.Analysis)
			}

			application := &App{
				config:              cfg,
				workDir:             t.TempDir(),
				client:              mock,
				registry:            registry,
				executor:            executor,
				taskRouter:          taskRouter,
				session:             chat.NewSession(),
				ctx:                 context.Background(),
				rateLimitRetryCount: make(map[string]int),
				autoResumeCount:     make(map[string]int),
			}

			application.processMessageWithContext(context.Background(), prompt)

			if got := runner.spawnCalls.Load(); got != 1 {
				t.Fatalf("sub-agent Spawn calls = %d, want exactly 1", got)
			}
			if len(application.rateLimitRetryCount) != 0 {
				t.Fatalf("unsafe failure consumed rate-limit retry budget: %v", application.rateLimitRetryCount)
			}
			if len(application.autoResumeCount) != 0 {
				t.Fatalf("unsafe failure consumed auto-resume budget: %v", application.autoResumeCount)
			}
			if got := len(application.session.GetPendingRecoveries()); got != 0 {
				t.Fatalf("unsafe failure published %d durable recovery generation(s)", got)
			}
			application.recoveryTimerMu.Lock()
			timers := len(application.recoveryTimers)
			application.recoveryTimerMu.Unlock()
			if timers != 0 {
				t.Fatalf("unsafe failure published %d recovery timer(s)", timers)
			}
			if application.lastError != "" {
				t.Fatalf("unsafe failure leaked automatic next-prompt context: %q", application.lastError)
			}
		})
	}
}

type retrySafetyProbe struct {
	cause error
	safe  bool
}

func (e *retrySafetyProbe) Error() string { return e.cause.Error() }
func (e *retrySafetyProbe) Unwrap() error { return e.cause }
func (e *retrySafetyProbe) AutomaticRetrySafe() bool {
	return e.safe
}

func TestAutomaticRetrySafetyMarkerSurvivesWrappingAndOverridesTaxonomy(t *testing.T) {
	unsafeTimeout := fmt.Errorf("policy wrapper: %w", &retrySafetyProbe{
		cause: client.ErrModelRoundTimeout,
		safe:  false,
	})
	if !errors.Is(unsafeTimeout, client.ErrModelRoundTimeout) {
		t.Fatal("test error lost its retryable underlying cause")
	}
	if !isAutomaticRetryUnsafe(unsafeTimeout) {
		t.Fatal("wrapped retry-unsafe marker was not recognized")
	}
	if isAutoResumableError(unsafeTimeout) {
		t.Fatal("ordinary timeout taxonomy overrode retry-unsafe side-effect provenance")
	}

	safeTimeout := &retrySafetyProbe{cause: client.ErrModelRoundTimeout, safe: true}
	if isAutomaticRetryUnsafe(safeTimeout) {
		t.Fatal("explicitly retry-safe marker was classified unsafe")
	}
	if !isAutoResumableError(safeTimeout) {
		t.Fatal("explicitly retry-safe timeout lost normal auto-resume behavior")
	}
}
