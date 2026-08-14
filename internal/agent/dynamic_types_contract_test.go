package agent

import (
	"reflect"
	"sync"
	"testing"
)

func TestAgentTypeRegistrySnapshotsAreDeterministicAndDefensive(t *testing.T) {
	registry := NewAgentTypeRegistry()
	initialRevision := registry.AgentTypeRevision()
	input := &DynamicAgentType{
		Name:         "z-reviewer",
		Description:  "Reviews changes",
		AllowedTools: []string{"read"},
	}
	if err := registry.RegisterDynamicType(input); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterDynamic("a-auditor", "Audits changes", []string{"grep"}, "audit"); err != nil {
		t.Fatal(err)
	}
	if got := registry.AgentTypeRevision(); got != initialRevision+2 {
		t.Fatalf("revision after registrations = %d, want %d", got, initialRevision+2)
	}

	input.Description = "mutated"
	input.AllowedTools[0] = "write"
	got, ok := registry.GetDynamic("z-reviewer")
	if !ok || got.Description != "Reviews changes" || !reflect.DeepEqual(got.AllowedTools, []string{"read"}) {
		t.Fatalf("registry retained caller-owned data: %+v", got)
	}
	got.AllowedTools[0] = "write"
	got.Description = "mutated again"
	again, _ := registry.GetDynamic("z-reviewer")
	if again.Description != "Reviews changes" || !reflect.DeepEqual(again.AllowedTools, []string{"read"}) {
		t.Fatalf("GetDynamic exposed registry-owned data: %+v", again)
	}

	names := registry.ListAll()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("ListAll is not deterministic: %v", names)
		}
	}
	snapshot := registry.SnapshotAgentTypes()
	for i := 1; i < len(snapshot); i++ {
		if snapshot[i-1].Name > snapshot[i].Name {
			t.Fatalf("SnapshotAgentTypes is not deterministic: %v", snapshot)
		}
	}
}

func TestAgentTypeRegistryRevisionChangesOnlyOnSuccessfulMutations(t *testing.T) {
	registry := NewAgentTypeRegistry()
	initial := registry.AgentTypeRevision()

	if err := registry.RegisterDynamic("explore", "conflict", nil, "prompt"); err == nil {
		t.Fatal("built-in override unexpectedly succeeded")
	}
	if err := registry.UnregisterDynamic("missing"); err == nil {
		t.Fatal("missing unregister unexpectedly succeeded")
	}
	if got := registry.AgentTypeRevision(); got != initial {
		t.Fatalf("failed mutations advanced revision from %d to %d", initial, got)
	}

	if err := registry.RegisterDynamic("reviewer", "Reviews", []string{"read"}, "prompt"); err != nil {
		t.Fatal(err)
	}
	afterRegister := registry.AgentTypeRevision()
	if afterRegister != initial+1 {
		t.Fatalf("successful registration revision = %d, want %d", afterRegister, initial+1)
	}
	if err := registry.UnregisterDynamic("reviewer"); err != nil {
		t.Fatal(err)
	}
	if got := registry.AgentTypeRevision(); got != afterRegister+1 {
		t.Fatalf("successful unregister revision = %d, want %d", got, afterRegister+1)
	}
}

func TestAgentTypeRegistryConcurrentCatalogReads(t *testing.T) {
	registry := NewAgentTypeRegistry()
	var wg sync.WaitGroup
	for writer := 0; writer < 2; writer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				_ = registry.RegisterDynamic("runtime", "Runtime agent", []string{"read"}, "prompt")
				_ = registry.UnregisterDynamic("runtime")
			}
		}()
	}
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_ = registry.ListAll()
				_ = registry.ListDynamic()
				_ = registry.SnapshotAgentTypes()
				_, _ = registry.GetDynamic("runtime")
				_ = registry.GetToolsForType("runtime")
				_ = registry.GetDescriptionForType("runtime")
			}
		}()
	}
	wg.Wait()
}
