package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/tools"
)

func TestDefaultMetaAgentStuckThresholdOutlivesModelRound(t *testing.T) {
	cfg := DefaultMetaAgentConfig()
	if got, want := cfg.StuckThreshold, config.ModelWatchdogTimeout(config.DefaultModelRoundTimeout); got != want {
		t.Fatalf("default stuck threshold = %v, want %v", got, want)
	}
}

func TestMetaAgentSetModelRoundTimeoutUpdatesLiveThreshold(t *testing.T) {
	ma := NewMetaAgent(context.Background(), nil, nil, nil, nil, nil)
	ma.SetModelRoundTimeout(40 * time.Minute)
	if got, want := ma.StuckThreshold(), 41*time.Minute; got != want {
		t.Fatalf("live stuck threshold = %v, want %v", got, want)
	}
}

func TestMetaAgentCopiesCallerConfig(t *testing.T) {
	cfg := DefaultMetaAgentConfig()
	ma := NewMetaAgent(context.Background(), nil, nil, nil, nil, cfg)
	cfg.StuckThreshold = time.Second
	if got, want := ma.StuckThreshold(), config.DefaultModelWatchdogFloor; got != want {
		t.Fatalf("caller mutation changed live config: got %v, want %v", got, want)
	}
}

func TestMetaAgentRequestsCancellationOncePerRegistration(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	var cancelCalls atomic.Int32
	tracked := &Agent{ID: "agent-once", Type: AgentTypeGeneral, status: AgentStatusRunning}
	tracked.SetCancelFunc(func() { cancelCalls.Add(1) })
	runner.mu.Lock()
	runner.agents[tracked.ID] = tracked
	runner.mu.Unlock()

	ma := NewMetaAgent(context.Background(), runner, nil, nil, nil, &MetaAgentConfig{
		Enabled:          true,
		CheckInterval:    time.Second,
		StuckThreshold:   time.Nanosecond,
		MaxInterventions: 0,
	})
	ma.RegisterAgent(tracked.ID, tracked.Type)
	ma.mu.Lock()
	ma.activeAgents[tracked.ID].LastActivity = time.Now().Add(-time.Minute)
	ma.mu.Unlock()

	ma.checkAgentHealth()
	ma.checkAgentHealth()

	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("runner cancellation calls = %d, want exactly 1", got)
	}
	monitor, ok := ma.GetAgentStatus(tracked.ID)
	if !ok || !monitor.CancelRequested {
		t.Fatalf("monitor cancellation latch = %+v, registered=%v", monitor, ok)
	}
}

func TestMetaAgentThresholdUpdatesRaceWithHealthChecks(t *testing.T) {
	ma := NewMetaAgent(context.Background(), nil, nil, nil, nil, nil)
	ma.RegisterAgent("agent-race", AgentTypeGeneral)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			ma.SetModelRoundTimeout(time.Duration(14+i%3) * time.Minute)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			ma.checkAgentHealth()
			_ = ma.GetStats()
		}
	}()
	wg.Wait()
}
