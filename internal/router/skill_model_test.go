package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type routerSkillClient struct{ *testkit.MockClient }

func (routerSkillClient) GetProvider() string { return "glm" }

func TestRouterSkillHandoffPromotesExactlyOnceAndRestoresNextTurn(t *testing.T) {
	for _, fields := range []string{"allowed-tools: Read", "allowed-tools: Read\neffort: high"} {
		t.Run(fields, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "review", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("---\n"+fields+"\n---\nReview code."), 0o600); err != nil {
				t.Fatal(err)
			}
			skill := tools.NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}))
			registry := tools.NewRegistry()
			registry.MustRegister(skill)
			mock := testkit.NewMockClient().EnqueueText("First.").EnqueueText("Second.")
			mock.SetModel("glm-5.3")
			c := routerSkillClient{mock}
			executor := tools.NewExecutor(registry, c, time.Second)
			router := NewRouter(&RouterConfig{Enabled: true, DecomposeThreshold: 100, ParallelThreshold: 100}, executor, nil, c, registry, false, root)
			loaded, err := skill.ExecuteForUserArgumentsContext(executor.SkillInvocationContext(t.Context()), "review", nil)
			if err != nil || !loaded.Success {
				t.Fatalf("load=%+v err=%v", loaded, err)
			}
			calls := 0
			mock.OnSend = func(context.Context) {
				calls++
				grants := skill.ActivePermissionGrants()
				settings := tools.SkillModelSettings(registry)
				if calls == 1 {
					if len(grants) != 1 || grants[0] != "read" {
						t.Errorf("handoff grants=%v", grants)
					}
					if fields != "allowed-tools: Read" && settings.Effort != "high" {
						t.Errorf("handoff settings=%+v", settings)
					}
				} else if len(grants) != 0 || settings.Effort != "" {
					t.Errorf("next turn retained grants/settings: %v %+v", grants, settings)
				}
			}
			// The ordinary greeting chooses the direct handler. With pending
			// settings it must remain in the owning executor instead of delegation.
			if _, _, err := router.Execute(t.Context(), nil, "hello"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := router.Execute(t.Context(), nil, "hello again"); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("model calls=%d", calls)
			}
		})
	}
}
