package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type handoffSkillClient struct{ *testkit.MockClient }

func (handoffSkillClient) GetProvider() string { return "glm" }

func TestCancelProcessingDiscardsQueuedSkillSettingsWithoutChangingActiveModel(t *testing.T) {
	for _, withOwner := range []bool{false, true} {
		name := "queued_without_owner"
		if withOwner {
			name = "cancelled_foreground"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for name, fields := range map[string]string{
				"active":  "model: haiku\neffort: max\nallowed-tools: Read",
				"pending": "model: inherit\neffort: low\nallowed-tools: Bash\ndisallowed-tools: Write",
			} {
				path := filepath.Join(root, name, "SKILL.md")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("---\n"+fields+"\n---\nReview code."), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			skill := tools.NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}))
			registry := tools.NewRegistry()
			registry.MustRegister(skill)
			mock := testkit.NewMockClient()
			mock.SetModel("glm-5.3")
			executor := tools.NewExecutor(registry, handoffSkillClient{mock}, time.Second)
			active, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "active"})
			if err != nil || !active.Success {
				t.Fatalf("active=%+v err=%v", active, err)
			}
			pending, err := skill.ExecuteForUserArgumentsContext(executor.SkillInvocationContext(t.Context()), "pending", nil)
			if err != nil || !pending.Success || !tools.HasPendingSkillModelSettings(registry) {
				t.Fatalf("pending=%+v err=%v", pending, err)
			}
			a := &App{registry: registry, ctx: context.Background()}
			var cancelledContext context.Context
			if withOwner {
				cancelledContext, a.processingCancel = context.WithCancel(t.Context())
			}
			if _, ok := a.enqueuePending("expanded skill prompt"); !ok {
				t.Fatal("could not enqueue skill continuation")
			}
			if !a.cancelProcessing() || a.pendingCount() != 0 {
				t.Fatal("cancellation did not discard the queued prompt")
			}
			if withOwner && cancelledContext.Err() == nil {
				t.Fatal("foreground context was not cancelled")
			}
			if tools.HasPendingSkillModelSettings(registry) {
				t.Fatal("cancelled skill settings remain pending")
			}
			if got := tools.SkillModelSettings(registry); got.Model != "glm-5.3-flash" || got.Effort != "max" {
				t.Fatalf("cancellation changed active request identity: %+v", got)
			}
			if got := skill.ActivePermissionGrants(); len(got) != 1 || got[0] != "read" {
				t.Fatalf("active scope changed before request completion: %v", got)
			}
			tools.BeginSkillPermissionTurn(registry)
			if got := tools.SkillModelSettings(registry); got != (client.RequestSettings{}) {
				t.Fatalf("cancelled settings activated in unrelated turn: %+v", got)
			}
			if len(skill.ActivePermissionGrants()) != 0 || len(skill.ActivePermissionDenies()) != 0 {
				t.Fatal("cancelled permissions activated in unrelated turn")
			}
			if len(skill.InvocationLedger().SnapshotNewestFirst()) != 2 {
				t.Fatal("discarding ephemeral settings lost retained instructions")
			}
		})
	}
}
