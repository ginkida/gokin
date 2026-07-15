package commands

import (
	"sync"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/mcp"
	"gokin/internal/testkit"
)

// mcpMutationTransactionProbe mirrors the real App's outer MCP mutation lock
// while exposing lifecycle checkpoints. The first commit blocks so the test can
// prove a sibling mutation cannot take its config snapshot until the complete
// snapshot -> runtime core/SetTools -> commit transaction has finished.
type mcpMutationTransactionProbe struct {
	*fakeAppForMCP

	mutationMu sync.Mutex
	stateMu    sync.Mutex
	mockClient *testkit.MockClient

	lockAttempts        int
	unlockCalls         int
	snapshotCalls       int
	commitCalls         int
	toolsPushedAtCommit []bool

	secondLockAttempted chan struct{}
	firstCommitEntered  chan struct{}
	releaseFirstCommit  chan struct{}
}

func (a *mcpMutationTransactionProbe) LockMCPConfigMutation() {
	a.stateMu.Lock()
	a.lockAttempts++
	attempt := a.lockAttempts
	a.stateMu.Unlock()
	if attempt == 2 {
		close(a.secondLockAttempted)
	}
	a.mutationMu.Lock()
}

func (a *mcpMutationTransactionProbe) UnlockMCPConfigMutation() {
	a.stateMu.Lock()
	a.unlockCalls++
	a.stateMu.Unlock()
	a.mutationMu.Unlock()
}

func (a *mcpMutationTransactionProbe) GetConfig() *config.Config {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.snapshotCalls++
	return a.cfg.Clone()
}

func (a *mcpMutationTransactionProbe) CommitMCPConfigSnapshot(cfg *config.Config) {
	a.stateMu.Lock()
	a.commitCalls++
	commit := a.commitCalls
	a.toolsPushedAtCommit = append(a.toolsPushedAtCommit, len(a.mockClient.GetTools()) > 0)
	a.stateMu.Unlock()

	if commit == 1 {
		close(a.firstCommitEntered)
		<-a.releaseFirstCommit
	}

	a.stateMu.Lock()
	a.cfg = cfg.Clone()
	a.stateMu.Unlock()
}

func TestMCPMutationLockCoversSnapshotCoreSetToolsAndCommit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mockClient := testkit.NewMockClient()
	base := &fakeAppForMCP{
		cfg: &config.Config{MCP: config.MCPConfig{
			Enabled: true,
			Servers: []config.MCPServerConfig{{Name: "alpha"}, {Name: "beta"}},
		}},
		mgr: mcp.NewManager([]*mcp.ServerConfig{
			{Name: "alpha"},
			{Name: "beta"},
		}),
		registry: nil,
		client:   mockClient,
	}
	// A real App always has a registry. Reuse the regular fake constructor's
	// registry so MCPRemoveCore reaches the declaration refresh path.
	base.registry = newFakeApp(t, nil).registry
	app := &mcpMutationTransactionProbe{
		fakeAppForMCP:       base,
		mockClient:          mockClient,
		secondLockAttempted: make(chan struct{}),
		firstCommitEntered:  make(chan struct{}),
		releaseFirstCommit:  make(chan struct{}),
	}
	defer withStubbedSave(t, base)()
	var releaseOnce sync.Once
	releaseFirstCommit := func() {
		releaseOnce.Do(func() { close(app.releaseFirstCommit) })
	}
	defer releaseFirstCommit()

	done := make(chan error, 2)
	go func() {
		_, err := MCPRemoveForApp(app.mgr, app, "alpha")
		done <- err
	}()
	waitForMCPMutationCheckpoint(t, app.firstCommitEntered, "first mutation did not reach commit")

	go func() {
		_, err := MCPRemoveForApp(app.mgr, app, "beta")
		done <- err
	}()
	waitForMCPMutationCheckpoint(t, app.secondLockAttempted, "second mutation did not attempt the lock")

	app.stateMu.Lock()
	snapshotsWhileFirstCommitBlocked := app.snapshotCalls
	app.stateMu.Unlock()
	if snapshotsWhileFirstCommitBlocked != 1 {
		t.Fatalf("second mutation took snapshot before first commit completed: snapshots=%d", snapshotsWhileFirstCommitBlocked)
	}

	releaseFirstCommit()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("MCP remove: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("serialized MCP mutation did not finish")
		}
	}

	app.stateMu.Lock()
	defer app.stateMu.Unlock()
	if app.lockAttempts != 2 || app.unlockCalls != 2 || app.snapshotCalls != 2 || app.commitCalls != 2 {
		t.Fatalf("incomplete mutation lifecycle: locks=%d unlocks=%d snapshots=%d commits=%d",
			app.lockAttempts, app.unlockCalls, app.snapshotCalls, app.commitCalls)
	}
	for i, pushed := range app.toolsPushedAtCommit {
		if !pushed {
			t.Fatalf("mutation %d reached config commit before SetTools", i+1)
		}
	}
	if len(app.cfg.MCP.Servers) != 0 {
		t.Fatalf("serialized sibling removals left stale config servers: %+v", app.cfg.MCP.Servers)
	}
}

func waitForMCPMutationCheckpoint(t *testing.T, checkpoint <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-checkpoint:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}
