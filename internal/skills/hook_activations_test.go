package skills

import (
	"fmt"
	"testing"
	"time"

	"gokin/internal/hooks"
)

func TestHookActivationEpochAndGenerationProtectRestoredOwner(t *testing.T) {
	ledger := NewInvocationLedger()
	activation, err := NewHookActivation("guard", "global", "/skills/guard/SKILL.md", []hooks.ClaudeCommand{{Event: "PreToolUse", Command: "exit 0", Once: true, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	_, epoch := ledger.HookActivations()
	_, _, registered, err := ledger.RecordSkillActivation("guard", "guard instructions", "global", activation.Path, &activation, epoch)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := ledger.HookActivations()
	ledger.ConsumeHook(registered.Path, registered.Generation, epoch, 0)
	used, _ := ledger.HookActivations()
	if !used[0].Consumed[0] || saved[0].Consumed[0] {
		t.Fatal("once snapshots alias live state")
	}
	if err := ledger.RestoreHookActivations(saved); err != nil {
		t.Fatal(err)
	}
	ledger.ConsumeHook(registered.Path, registered.Generation, epoch, 0)
	fresh, freshEpoch := ledger.HookActivations()
	if fresh[0].Consumed[0] {
		t.Fatal("old completion consumed a restored owner's hook")
	}
	ledger.ConsumeHook(registered.Path, "ffffffffffffffffffffffffffffffff", freshEpoch, 0)
	fresh, _ = ledger.HookActivations()
	if fresh[0].Consumed[0] {
		t.Fatal("replacement generation accepted an old completion")
	}
	ledger.ConsumeHook(registered.Path, registered.Generation, freshEpoch, 0)
	replacement, _ := NewHookActivation("guard", "global", activation.Path, []hooks.ClaudeCommand{{Event: "PreToolUse", Command: "exit 0", Once: true, Timeout: time.Second}})
	_, _, registered, err = ledger.RecordSkillActivation("guard", "guard instructions", "global", activation.Path, &replacement, freshEpoch)
	if err != nil || !registered.Consumed[0] || registered.Generation != activation.Generation {
		t.Fatalf("identical invocation rearmed once: %+v %v", registered, err)
	}
	ledger.Clear()
	if _, _, _, err := ledger.RecordSkillActivation("guard", "late instructions", "global", activation.Path, &activation, freshEpoch); err == nil || len(ledger.SnapshotNewestFirst()) != 0 {
		t.Fatal("old render crossed a clear boundary")
	}
}

func TestHookReferencesSurviveInstructionEvictionAndRejectCorruptionAtomically(t *testing.T) {
	ledger := NewInvocationLedger()
	activation, _ := NewHookActivation("guard", "global", "/skills/guard/SKILL.md", []hooks.ClaudeCommand{{Event: "PreToolUse", Command: "exit 2", Timeout: time.Second}})
	_, epoch := ledger.HookActivations()
	if _, _, _, err := ledger.RecordSkillActivation("guard", "guard", "global", activation.Path, &activation, epoch); err != nil {
		t.Fatal(err)
	}
	for i := range MaxSkillCount {
		if _, _, err := ledger.Record(fmt.Sprintf("skill-%d", i), "instructions", "global", "/skills/other"); err != nil {
			t.Fatal(err)
		}
	}
	saved, _ := ledger.HookActivations()
	if len(saved) != 1 {
		t.Fatal("instruction eviction removed enforced hooks")
	}
	bad := CloneHookActivations(saved)
	bad[0].DefinitionHash = "invalid"
	if err := ledger.RestoreHookActivations(bad); err == nil {
		t.Fatal("corrupt references accepted")
	}
	after, afterEpoch := ledger.HookActivations()
	if len(after) != 1 || after[0].DefinitionHash != activation.DefinitionHash || afterEpoch != epoch {
		t.Fatal("invalid restore mutated hook ownership")
	}
	if err := ledger.RestoreHookActivations(append(saved, saved...)); err == nil {
		t.Fatal("duplicate references accepted")
	}
	bad = CloneHookActivations(saved)
	bad[0].Consumed = make([]bool, 33)
	if err := ledger.RestoreHookActivations(bad); err == nil {
		t.Fatal("unbounded handler state accepted")
	}
}
