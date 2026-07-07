package agent

import (
	"sync"
	"testing"

	"gokin/internal/tools"
)

// TestSaveCheckpoint_ScratchpadRaceWithUpdate (round 5) pins the fix:
// SaveCheckpoint used to read a.Scratchpad with no lock at all, while the
// update_scratchpad tool's updater closure (agent.go) writes it under
// a.stateMu.Lock() — every OTHER access site (e.g. buildSystemPrompt) reads
// it under a.stateMu.RLock(). A concurrent write (as can happen via
// executeToolsParallel's documented abandoned-straggler path, v0.100.60)
// racing an unlocked SaveCheckpoint read is a genuine -race hit.
func TestSaveCheckpoint_ScratchpadRaceWithUpdate(t *testing.T) {
	agent := NewAgent(AgentTypeGeneral, nil, tools.NewRegistry(), t.TempDir(), 3, "", nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			agent.stateMu.Lock()
			agent.Scratchpad = "concurrent update"
			agent.stateMu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := agent.SaveCheckpoint("test"); err != nil {
				t.Errorf("SaveCheckpoint: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}
