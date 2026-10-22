package chat

import (
	"encoding/json"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/skills"
	"google.golang.org/genai"
)

func TestSessionHookStateRoundTripCheckpointAndForkIsolation(t *testing.T) {
	session := NewSession()
	session.SetHistory([]*genai.Content{genai.NewContentFromText("review", genai.RoleUser), genai.NewContentFromText("ready", genai.RoleModel)})
	session.SaveCheckpoint("before")
	activation, err := skills.NewHookActivation("guard", "global", "/skills/guard/SKILL.md", []hooks.ClaudeCommand{{Event: "PreToolUse", Command: "exit 0", Once: true, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	_, epoch := session.InvocationLedger().HookActivations()
	_, _, registered, err := session.InvocationLedger().RecordSkillActivation("guard", "guard instructions", "global", activation.Path, &activation, epoch)
	if err != nil {
		t.Fatal(err)
	}
	session.SaveCheckpoint("loaded")
	session.InvocationLedger().ConsumeHook(registered.Path, registered.Generation, epoch, 0)
	branch := session.Fork("alternate")
	refs, branchEpoch := branch.InvocationLedger().HookActivations()
	if len(refs) != 1 || !refs[0].Consumed[0] {
		t.Fatal("fork dropped once state")
	}
	refs[0].Consumed[0] = false
	if err := branch.InvocationLedger().RestoreHookActivations(refs); err != nil {
		t.Fatal(err)
	}
	parentRefs, _ := session.InvocationLedger().HookActivations()
	if !parentRefs[0].Consumed[0] {
		t.Fatal("fork mutated parent hook state")
	}
	branch.InvocationLedger().ConsumeHook(registered.Path, registered.Generation, branchEpoch, 0)
	refs, _ = branch.InvocationLedger().HookActivations()
	if refs[0].Consumed[0] {
		t.Fatal("stale fork completion crossed restore boundary")
	}
	encoded, err := json.Marshal(session.GetState())
	if err != nil {
		t.Fatal(err)
	}
	var state SessionState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	restored := NewSession()
	if err := restored.RestoreFromState(&state); err != nil {
		t.Fatal(err)
	}
	snapshot := restored.GetState()
	snapshot.SkillHooks[0].Consumed[0] = false
	snapshot.CheckpointSkillHooks["loaded"][0].Consumed[0] = true
	refs, _ = restored.InvocationLedger().HookActivations()
	if !refs[0].Consumed[0] {
		t.Fatal("snapshot aliases active once state")
	}
	if !restored.RestoreCheckpoint("loaded") {
		t.Fatal("restore loaded checkpoint")
	}
	refs, _ = restored.InvocationLedger().HookActivations()
	if len(refs) != 1 || refs[0].Consumed[0] {
		t.Fatal("checkpoint did not restore its once state")
	}
	if !restored.RestoreCheckpoint("before") {
		t.Fatal("restore pre-skill checkpoint")
	}
	refs, _ = restored.InvocationLedger().HookActivations()
	if len(refs) != 0 {
		t.Fatal("post-checkpoint hooks survived rewind")
	}
	if len(restored.GetState().Branches["alternate"].SkillHooks) != 1 {
		t.Fatal("rewind damaged an independent branch")
	}
}

func TestInvalidHookGraphCannotPartiallyPublishSession(t *testing.T) {
	activation, _ := skills.NewHookActivation("guard", "global", "/skills/guard/SKILL.md", []hooks.ClaudeCommand{{Event: "PreToolUse", Command: "exit 2", Timeout: time.Second}})
	activation.DefinitionHash = "corrupt"
	for _, scenario := range []string{"active", "checkpoint", "branch", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			session := NewSession()
			session.SetHistory([]*genai.Content{genai.NewContentFromText("current work", genai.RoleUser)})
			previous := session.GetState()
			candidate := NewSession().GetState()
			switch scenario {
			case "active":
				candidate.SkillHooks = []skills.HookActivation{activation}
			case "checkpoint":
				candidate.CheckpointSkillHooks = map[string][]skills.HookActivation{"bad": {activation}}
			case "branch":
				candidate.Branches = map[string]*SessionState{"bad": {SkillHooks: []skills.HookActivation{activation}}}
			case "cycle":
				candidate.Branches = map[string]*SessionState{"cycle": candidate}
			}
			if err := session.RestoreFromState(candidate); err == nil {
				t.Fatal("invalid hook graph accepted")
			}
			after := session.GetState()
			if after.ID != previous.ID || len(after.History) != len(previous.History) {
				t.Fatal("invalid graph partially replaced owner/history")
			}
		})
	}
}
