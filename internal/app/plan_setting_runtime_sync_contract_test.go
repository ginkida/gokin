package app

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"gokin/internal/agent"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/plan"
	"gokin/internal/router"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

// TestApplyConfigSynchronizesPlanModeRuntime pins the shared contract used by
// both `/set plan` and the interactive `/settings` toggle: cfg.Plan.Enabled is
// authoritative and must reach every long-lived runtime consumer in-process.
// Otherwise those surfaces report success while the manager/router/runner keep
// executing in the previous mode until restart.
func TestApplyConfigSynchronizesPlanModeRuntime(t *testing.T) {
	for _, want := range []bool{true, false} {
		want := want
		t.Run(map[bool]string{true: "enable", false: "disable"}[want], func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			ctx := context.Background()
			workDir := t.TempDir()
			cfg := config.DefaultConfig()
			cfg.API.ActiveProvider = "glm"
			cfg.API.Backend = "glm"
			cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
			cfg.Model.Provider = "glm"
			cfg.Model.Name = "glm-5.2"
			cfg.Plan.Enabled = want

			registry := tools.DefaultRegistry(workDir)
			executor := tools.NewExecutor(registry, nil, 30*time.Second)
			runner := agent.NewRunner(ctx, nil, registry, workDir)
			runner.SetPlanningModeEnabled(!want)
			manager := plan.NewManager(!want, cfg.Plan.RequireApproval)
			taskRouter := router.NewRouter(
				&router.RouterConfig{Enabled: true}, executor, runner, nil, registry, false, workDir,
			)
			taskRouter.SetPlanMode(!want)
			promptBuilder := appcontext.NewPromptBuilder(workDir, nil)
			promptBuilder.SetPlanMode(!want)
			tuiModel := ui.NewModel()
			tuiModel.SetPlanningModeEnabled(!want)

			a := &App{
				config:              cfg,
				ctx:                 ctx,
				workDir:             workDir,
				registry:            registry,
				executor:            executor,
				agentRunner:         runner,
				planManager:         manager,
				taskRouter:          taskRouter,
				promptBuilder:       promptBuilder,
				tui:                 tuiModel,
				planningModeEnabled: !want,
			}

			if err := a.ApplyConfig(cfg); err != nil {
				t.Fatalf("ApplyConfig: %v", err)
			}

			if got := a.IsPlanningModeEnabled(); got != want {
				t.Errorf("app plan mode = %v, want cfg.Plan.Enabled=%v", got, want)
			}
			if got := manager.IsEnabled(); got != want {
				t.Errorf("plan manager enabled = %v, want %v", got, want)
			}
			if got := runner.IsPlanningModeEnabled(); got != want {
				t.Errorf("agent runner plan mode = %v, want %v", got, want)
			}
			if got := reflectedRouterPlanMode(taskRouter); got != want {
				t.Errorf("router plan mode = %v, want %v", got, want)
			}
			if got := tuiModel.GetPlanningModeEnabled(); got != want {
				t.Errorf("TUI plan mode = %v, want %v", got, want)
			}
			if got := strings.Contains(promptBuilder.Build(), "## PLAN MODE"); got != want {
				t.Errorf("system prompt plan banner present = %v, want %v", got, want)
			}
			if got, expected := reflectedClientToolNames(a), toolNames(a.planModeToolsLocked(want)); !reflect.DeepEqual(got, expected) {
				t.Errorf("client tool schema = %v, want %v for plan mode %v", got, expected, want)
			}
			if got := a.settingToggleSnapshotLocked()["plan"]; got != want {
				t.Errorf("ConfigUpdate.Settings[plan] = %v, want %v", got, want)
			}
		})
	}
}

// TestPlanModeTransitionsKeepConfigSnapshotAligned prevents `/plan`,
// Shift+Tab, and approval from leaving cfg.Plan.Enabled behind the live mode.
// Once ApplyConfig honors that field, a stale snapshot would otherwise undo a
// user's mode transition on the next unrelated model/provider/setting change.
func TestPlanModeTransitionsKeepConfigSnapshotAligned(t *testing.T) {
	t.Run("toggle", func(t *testing.T) {
		cfg := &config.Config{}
		a := &App{config: cfg}

		if got := a.TogglePlanningMode(); !got {
			t.Fatal("TogglePlanningMode returned disabled, want enabled")
		}
		if !cfg.Plan.Enabled {
			t.Fatal("TogglePlanningMode did not synchronize cfg.Plan.Enabled")
		}
		if got := a.settingToggleSnapshotLocked()["plan"]; !got {
			t.Fatal("toggle snapshot still reports plan off")
		}
	})

	t.Run("approval exits plan mode", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Plan.Enabled = true
		a := &App{
			config:              cfg,
			planningModeEnabled: true,
			planManager:         plan.NewManager(true, true),
		}

		a.disablePlanModeAfterApproval()

		if cfg.Plan.Enabled {
			t.Fatal("plan approval left cfg.Plan.Enabled on after runtime exited plan mode")
		}
		if got := a.settingToggleSnapshotLocked()["plan"]; got {
			t.Fatal("approval snapshot still reports plan on")
		}
	})
}

// Router deliberately keeps its plan flag private; this package-level
// integration test reads the boolean only so ApplyConfig's cross-package
// propagation remains covered without widening production API surface.
func reflectedRouterPlanMode(r *router.Router) bool {
	return reflect.ValueOf(r).Elem().FieldByName("planMode").Bool()
}

func reflectedClientToolNames(a *App) []string {
	toolsValue := reflect.ValueOf(a.GetMainClient()).Elem().FieldByName("tools")
	names := make([]string, 0)
	for i := 0; i < toolsValue.Len(); i++ {
		toolValue := toolsValue.Index(i)
		if toolValue.IsNil() {
			continue
		}
		declarations := toolValue.Elem().FieldByName("FunctionDeclarations")
		for j := 0; j < declarations.Len(); j++ {
			declaration := declarations.Index(j)
			if declaration.IsNil() {
				continue
			}
			names = append(names, declaration.Elem().FieldByName("Name").String())
		}
	}
	sort.Strings(names)
	return names
}

func toolNames(toolSets []*genai.Tool) []string {
	names := make([]string, 0)
	for _, toolSet := range toolSets {
		if toolSet == nil {
			continue
		}
		for _, declaration := range toolSet.FunctionDeclarations {
			if declaration != nil {
				names = append(names, declaration.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}
