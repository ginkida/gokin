package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/tools"

	"google.golang.org/genai"
)

type exampleLearnCall struct {
	taskType  string
	prompt    string
	agentType string
	output    string
	duration  time.Duration
	tokens    int
}

type fakeExampleStore struct {
	calls chan exampleLearnCall
}

func (f *fakeExampleStore) LearnFromSuccess(taskType, prompt, agentType, output string, duration time.Duration, tokens int) error {
	f.calls <- exampleLearnCall{
		taskType:  taskType,
		prompt:    prompt,
		agentType: agentType,
		output:    output,
		duration:  duration,
		tokens:    tokens,
	}
	return nil
}

func (f *fakeExampleStore) GetSimilarExamples(string, int) []TaskExampleSummary {
	return nil
}

func (f *fakeExampleStore) GetExamplesForContext(string, string, int) string {
	return ""
}

func runGit(t *testing.T, workDir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
	return string(output)
}

func initGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()

	workDir := t.TempDir()
	runGit(t, workDir, "init")
	for path, content := range files {
		fullPath := filepath.Join(workDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", fullPath, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile(%q): %v", fullPath, err)
		}
	}
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	return workDir
}

func TestSpawnAsyncPropagatesConfiguredCapabilities(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	sharedMemory := NewSharedMemory()
	runner.SetSharedMemory(sharedMemory)
	runner.SetSharedScratchpad("initial scratchpad")
	scratchpadUpdates := make(chan string, 1)
	runner.SetOnScratchpadUpdate(func(content string) {
		scratchpadUpdates <- content
	})
	runner.SetTreePlanner(NewTreePlanner(DefaultTreePlannerConfig(), nil, nil, nil))
	runner.SetPlanningModeEnabled(true)
	runner.SetRequireApprovalEnabled(true)
	runner.SetOnPlanApproved(func(string) {})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	agentID := runner.SpawnAsync(ctx, "general", "inspect runtime wiring", 4, "")

	runner.mu.RLock()
	agent := runner.agents[agentID]
	runner.mu.RUnlock()

	if agent == nil {
		t.Fatal("expected spawned agent to be registered")
	}
	if agent.GetSharedMemory() != sharedMemory {
		t.Fatal("expected shared memory to be propagated")
	}
	if agent.Scratchpad != "initial scratchpad" {
		t.Fatalf("scratchpad = %q, want initial scratchpad", agent.Scratchpad)
	}
	if agent.onScratchpadUpdate == nil {
		t.Fatal("expected scratchpad callback to be configured")
	}
	if !agent.IsPlanningMode() {
		t.Fatal("expected planning mode to be enabled")
	}
	if !agent.requireApproval {
		t.Fatal("expected require approval to be enabled")
	}
	if agent.onPlanApproved == nil {
		t.Fatal("expected plan approval callback to be configured")
	}
	if agent.treePlanner == nil {
		t.Fatal("expected tree planner to be configured")
	}
	if agent.treePlanner == runner.GetTreePlanner() {
		t.Fatal("expected each agent to receive its own tree planner clone")
	}

	agent.onScratchpadUpdate("updated scratchpad")

	runner.mu.RLock()
	sharedScratchpad := runner.sharedScratchpad
	runner.mu.RUnlock()
	if sharedScratchpad != "updated scratchpad" {
		t.Fatalf("runner scratchpad = %q, want updated scratchpad", sharedScratchpad)
	}

	select {
	case got := <-scratchpadUpdates:
		if got != "updated scratchpad" {
			t.Fatalf("scratchpad callback got %q, want updated scratchpad", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scratchpad callback")
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	result, err := runner.WaitWithContext(waitCtx, agentID)
	if err != nil {
		t.Fatalf("WaitWithContext: %v", err)
	}
	if result.Status != AgentStatusCancelled {
		t.Fatalf("status = %s, want %s", result.Status, AgentStatusCancelled)
	}
	if result.Type != agent.Type {
		t.Fatalf("result type = %s, want %s", result.Type, agent.Type)
	}
}

func TestSpawnAsyncWithStreamingSupportsDynamicTypes(t *testing.T) {
	workDir := t.TempDir()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(workDir))
	registry.MustRegister(tools.NewBashTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic("reviewer", "Reviews code with read-only access", []string{"read"}, "dynamic reviewer prompt"); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}
	runner.SetTypeRegistry(typeRegistry)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	agentID := runner.SpawnAsyncWithStreaming(ctx, "reviewer", "review this code", 3, "", func(string) {}, nil)

	runner.mu.RLock()
	agent := runner.agents[agentID]
	runner.mu.RUnlock()

	if agent == nil {
		t.Fatal("expected spawned agent to be registered")
	}
	if agent.Type != AgentType("reviewer") {
		t.Fatalf("agent type = %s, want reviewer", agent.Type)
	}
	if agent.projectContext != "dynamic reviewer prompt" {
		t.Fatalf("project context = %q, want dynamic reviewer prompt", agent.projectContext)
	}
	if agent.onText == nil {
		t.Fatal("expected streaming callback to be configured")
	}
	if _, ok := agent.registry.Get("read"); !ok {
		t.Fatal("expected dynamic type to keep allowed read tool")
	}
	if _, ok := agent.registry.Get("bash"); ok {
		t.Fatal("expected dynamic type to exclude disallowed bash tool")
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	result, err := runner.WaitWithContext(waitCtx, agentID)
	if err != nil {
		t.Fatalf("WaitWithContext: %v", err)
	}
	if result.Status != AgentStatusCancelled {
		t.Fatalf("status = %s, want %s", result.Status, AgentStatusCancelled)
	}
	if result.Type != AgentType("reviewer") {
		t.Fatalf("result type = %s, want reviewer", result.Type)
	}
}

func TestSpawnMultiplePropagatesConfiguredCapabilitiesAndDynamicTypes(t *testing.T) {
	workDir := t.TempDir()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(workDir))
	registry.MustRegister(tools.NewBashTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic("reviewer", "Reviews code with read-only access", []string{"read"}, "dynamic reviewer prompt"); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}
	runner.SetTypeRegistry(typeRegistry)

	sharedMemory := NewSharedMemory()
	runner.SetSharedMemory(sharedMemory)
	runner.SetSharedScratchpad("shared review notes")
	runner.SetTreePlanner(NewTreePlanner(DefaultTreePlannerConfig(), nil, nil, nil))
	runner.SetPlanningModeEnabled(true)
	runner.SetRequireApprovalEnabled(true)
	runner.SetOnPlanApproved(func(string) {})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ids, _ := runner.SpawnMultiple(ctx, []AgentTask{{
		Prompt:   "review this code",
		Type:     AgentType("reviewer"),
		MaxTurns: 3,
	}})

	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("ids = %v, want one populated ID", ids)
	}

	runner.mu.RLock()
	agent := runner.agents[ids[0]]
	result := runner.results[ids[0]]
	runner.mu.RUnlock()

	if agent == nil {
		t.Fatal("expected spawned agent to be registered")
	}
	if agent.Type != AgentType("reviewer") {
		t.Fatalf("agent type = %s, want reviewer", agent.Type)
	}
	if agent.GetSharedMemory() != sharedMemory {
		t.Fatal("expected shared memory to be propagated")
	}
	if agent.Scratchpad != "shared review notes" {
		t.Fatalf("scratchpad = %q, want shared review notes", agent.Scratchpad)
	}
	if !agent.IsPlanningMode() {
		t.Fatal("expected planning mode to be enabled")
	}
	if !agent.requireApproval {
		t.Fatal("expected require approval to be enabled")
	}
	if agent.treePlanner == nil {
		t.Fatal("expected tree planner to be configured")
	}
	if agent.treePlanner == runner.GetTreePlanner() {
		t.Fatal("expected each agent to receive its own tree planner clone")
	}
	if _, ok := agent.registry.Get("read"); !ok {
		t.Fatal("expected dynamic type to keep allowed read tool")
	}
	if _, ok := agent.registry.Get("bash"); ok {
		t.Fatal("expected dynamic type to exclude disallowed bash tool")
	}
	if result == nil {
		t.Fatal("expected agent result to be recorded")
	}
	if result.Type != AgentType("reviewer") {
		t.Fatalf("result type = %s, want reviewer", result.Type)
	}
}

func TestRunPreservesExistingHistory(t *testing.T) {
	agent := NewAgent(AgentTypeGeneral, nil, tools.NewRegistry(), t.TempDir(), 3, "", nil, nil)
	restoredHistory := []*genai.Content{
		genai.NewContentFromText("restored system context", genai.RoleUser),
		genai.NewContentFromText("restored model acknowledgement", genai.RoleModel),
	}

	agent.stateMu.Lock()
	agent.history = restoredHistory
	agent.stateMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _ = agent.Run(ctx, "continue the restored task")

	agent.stateMu.RLock()
	defer agent.stateMu.RUnlock()

	if len(agent.history) < 3 {
		t.Fatalf("history length = %d, want at least 3", len(agent.history))
	}
	if got := agent.history[0].Parts[0].Text; got != "restored system context" {
		t.Fatalf("first history entry = %q, want restored system context", got)
	}
	if got := agent.history[len(agent.history)-1].Parts[0].Text; got != "continue the restored task" {
		t.Fatalf("last history entry = %q, want continue the restored task", got)
	}
}

func TestReadOnlyAgentsUseIsolatedWorkspace(t *testing.T) {
	workDir := t.TempDir()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	runner.SetWorkspaceIsolationEnabled(true)

	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic("reviewer", "Reviews code with read-only access", []string{"read"}, "dynamic reviewer prompt"); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}
	runner.SetTypeRegistry(typeRegistry)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	agentID, _ := runner.Spawn(ctx, "reviewer", "review this code", 3, "")

	runner.mu.RLock()
	agent := runner.agents[agentID]
	result := runner.results[agentID]
	runner.mu.RUnlock()

	if agent == nil {
		t.Fatal("expected spawned agent to be registered")
	}
	if agent.workDir == workDir {
		t.Fatalf("agent workDir = %q, want isolated workspace", agent.workDir)
	}
	if agent.isolatedWorkspace == nil {
		t.Fatal("expected isolated workspace to be attached")
	}
	if result == nil {
		t.Fatal("expected result to be recorded")
	}
	if isolated, ok := result.Metadata["isolated_workspace"].(bool); !ok || !isolated {
		t.Fatalf("isolated_workspace metadata = %v, want true", result.Metadata["isolated_workspace"])
	}
	if got := result.Metadata["isolated_workspace_strategy"]; got != "copy" {
		t.Fatalf("isolated_workspace_strategy = %v, want copy", got)
	}
	if got := result.Metadata["isolated_workspace_dir"]; got != agent.workDir {
		t.Fatalf("isolated_workspace_dir = %v, want %q", got, agent.workDir)
	}
	if _, err := os.Stat(agent.workDir); err != nil {
		t.Fatalf("expected isolated workspace to exist after failed run: %v", err)
	}
}

func TestReadOnlyIsolatedAgentsRestrictRequestedTools(t *testing.T) {
	workDir := t.TempDir()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(workDir))
	registry.MustRegister(tools.NewWriteTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	runner.SetWorkspaceIsolationEnabled(true)

	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic("reviewer", "Reviews code with read-only access", []string{"read"}, "dynamic reviewer prompt"); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}
	runner.SetTypeRegistry(typeRegistry)

	agent := runner.newConfiguredAgent(context.Background(), runner.snapshotAgentDeps(), "reviewer", 3, "", nil)
	if agent.isolatedWorkspace == nil {
		t.Fatal("expected isolated workspace")
	}
	if err := agent.RequestTool("write"); err == nil {
		t.Fatal("expected write tool request to be rejected for isolated read-only agent")
	}
}

func TestMutatingNonGitAgentsStayOnPrimaryWorkspace(t *testing.T) {
	workDir := t.TempDir()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(workDir))
	registry.MustRegister(tools.NewWriteTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	runner.SetWorkspaceIsolationEnabled(true)

	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic("writer", "Writes files", []string{"read", "write"}, "dynamic writer prompt"); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}
	runner.SetTypeRegistry(typeRegistry)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	agentID, _ := runner.Spawn(ctx, "writer", "write a change", 3, "")

	runner.mu.RLock()
	agent := runner.agents[agentID]
	result := runner.results[agentID]
	runner.mu.RUnlock()

	if agent == nil {
		t.Fatal("expected spawned agent to be registered")
	}
	if agent.workDir != workDir {
		t.Fatalf("agent workDir = %q, want primary workspace %q", agent.workDir, workDir)
	}
	if agent.isolatedWorkspace != nil {
		t.Fatal("did not expect isolated workspace for mutating agent")
	}
	if result != nil && result.Metadata != nil {
		if isolated, ok := result.Metadata["isolated_workspace"]; ok && isolated == true {
			t.Fatalf("unexpected isolated_workspace metadata: %#v", result.Metadata)
		}
	}
}

func TestBashAgentsUseManagedIsolatedWorkspace(t *testing.T) {
	workDir := initGitRepo(t, map[string]string{
		"tracked.txt": "original\n",
	})
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewBashTool(workDir))
	registry.MustRegister(tools.NewReadTool(workDir))
	registry.MustRegister(tools.NewGlobTool(workDir))
	registry.MustRegister(tools.NewWriteTool(workDir))
	registry.MustRegister(tools.NewGitCommitTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	runner.SetWorkspaceIsolationEnabled(true)

	agent := runner.newConfiguredAgent(context.Background(), runner.snapshotAgentDeps(), "bash", 3, "", nil)
	if agent.isolatedWorkspace == nil {
		t.Fatal("expected isolated workspace for bash agent")
	}
	if !agent.isolatedWorkspace.ApplyBackOnSuccess {
		t.Fatal("expected apply-back isolation for bash agent")
	}

	bashAny, ok := agent.registry.Get("bash")
	if !ok {
		t.Fatal("expected bash tool in agent registry")
	}
	bashTool, ok := bashAny.(*tools.BashTool)
	if !ok {
		t.Fatalf("bash tool type = %T, want *tools.BashTool", bashAny)
	}
	if !bashTool.ManagedWorkspaceApplyBackModeEnabled() {
		t.Fatal("expected managed apply-back mode for isolated bash tool")
	}

	result, err := bashTool.Execute(context.Background(), map[string]any{"command": "cd .."})
	if err != nil {
		t.Fatalf("Execute(cd ..): %v", err)
	}
	if result.Success {
		t.Fatalf("expected workspace boundary violation, got success: %#v", result)
	}
	if !strings.Contains(result.Error, "code 98") {
		t.Fatalf("result error = %q, want boundary exit", result.Error)
	}

	result, err = bashTool.Execute(context.Background(), map[string]any{
		"command":           "echo hi",
		"run_in_background": true,
	})
	if err != nil {
		t.Fatalf("Execute(run_in_background): %v", err)
	}
	if result.Success {
		t.Fatalf("expected background execution to be blocked: %#v", result)
	}
	if !strings.Contains(result.Error, "run_in_background is not supported") {
		t.Fatalf("background error = %q, want isolated apply-back error", result.Error)
	}

	result, err = bashTool.Execute(context.Background(), map[string]any{"command": "git commit -m test"})
	if err != nil {
		t.Fatalf("Execute(git commit): %v", err)
	}
	if result.Success {
		t.Fatalf("expected git commit to be blocked in managed mode: %#v", result)
	}
	if !strings.Contains(result.Error, "blocked in isolated apply-back mode") {
		t.Fatalf("git commit error = %q, want managed mode block", result.Error)
	}

	if err := agent.RequestTool("git_commit"); err == nil {
		t.Fatal("expected git_commit request to be rejected for isolated bash agent")
	}
	if err := agent.RequestTool("write"); err != nil {
		t.Fatalf("expected write request to be allowed in apply-back isolation: %v", err)
	}
}

func TestMutatingGitAgentsApplyBackFromIsolatedWorkspace(t *testing.T) {
	workDir := initGitRepo(t, map[string]string{
		"tracked.txt": "original\n",
		"delete.txt":  "remove me\n",
	})
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(workDir))
	registry.MustRegister(tools.NewWriteTool(workDir))
	registry.MustRegister(tools.NewEditTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	runner.SetWorkspaceIsolationEnabled(true)
	reviewCalls := 0
	runner.SetWorkspaceReviewHandler(func(_ context.Context, changes []WorkspaceChangePreview) (bool, error) {
		reviewCalls++
		if len(changes) != 3 {
			t.Fatalf("review changes = %d, want 3", len(changes))
		}
		found := make(map[string]WorkspaceChangePreview, len(changes))
		for _, change := range changes {
			found[change.FilePath] = change
		}
		if got := found["tracked.txt"].OldContent; got != "original\n" {
			t.Fatalf("tracked old content = %q, want original", got)
		}
		if got := found["tracked.txt"].NewContent; got != "updated\n" {
			t.Fatalf("tracked new content = %q, want updated", got)
		}
		if !found["new.txt"].IsNewFile {
			t.Fatal("expected new.txt to be marked as new")
		}
		if got := found["delete.txt"].NewContent; got != "" {
			t.Fatalf("delete new content = %q, want empty", got)
		}
		return true, nil
	})

	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic("writer", "Writes files", []string{"read", "write", "edit"}, "dynamic writer prompt"); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}
	runner.SetTypeRegistry(typeRegistry)

	agent := runner.newConfiguredAgent(context.Background(), runner.snapshotAgentDeps(), "writer", 3, "", nil)
	if agent.workDir == workDir {
		t.Fatalf("agent workDir = %q, want isolated git worktree", agent.workDir)
	}
	if agent.isolatedWorkspace == nil {
		t.Fatal("expected isolated workspace to be attached")
	}
	if !agent.isolatedWorkspace.ApplyBackOnSuccess {
		t.Fatal("expected apply-back to be enabled for git mutating agent")
	}
	if agent.isolatedWorkspace.Strategy != "git_worktree" {
		t.Fatalf("strategy = %q, want git_worktree", agent.isolatedWorkspace.Strategy)
	}

	if err := os.WriteFile(filepath.Join(agent.workDir, "tracked.txt"), []byte("updated\n"), 0644); err != nil {
		t.Fatalf("WriteFile(tracked.txt): %v", err)
	}
	if err := os.WriteFile(filepath.Join(agent.workDir, "new.txt"), []byte("new file\n"), 0644); err != nil {
		t.Fatalf("WriteFile(new.txt): %v", err)
	}
	if err := os.Remove(filepath.Join(agent.workDir, "delete.txt")); err != nil {
		t.Fatalf("Remove(delete.txt): %v", err)
	}

	result := &AgentResult{
		AgentID:   agent.ID,
		Type:      agent.Type,
		Status:    AgentStatusCompleted,
		Completed: true,
	}
	if err := runner.finalizeAgentWorkspace(agent, result); err != nil {
		t.Fatalf("finalizeAgentWorkspace: %v", err)
	}

	trackedContent, err := os.ReadFile(filepath.Join(workDir, "tracked.txt"))
	if err != nil {
		t.Fatalf("ReadFile(tracked.txt): %v", err)
	}
	if string(trackedContent) != "updated\n" {
		t.Fatalf("tracked.txt = %q, want updated", string(trackedContent))
	}

	newContent, err := os.ReadFile(filepath.Join(workDir, "new.txt"))
	if err != nil {
		t.Fatalf("ReadFile(new.txt): %v", err)
	}
	if string(newContent) != "new file\n" {
		t.Fatalf("new.txt = %q, want new file", string(newContent))
	}

	if _, err := os.Stat(filepath.Join(workDir, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete.txt should be removed, stat err = %v", err)
	}

	if isolated, ok := result.Metadata["isolated_workspace"].(bool); !ok || !isolated {
		t.Fatalf("isolated_workspace metadata = %v, want true", result.Metadata["isolated_workspace"])
	}
	if applyBack, ok := result.Metadata["isolated_workspace_apply_back"].(bool); !ok || !applyBack {
		t.Fatalf("isolated_workspace_apply_back = %v, want true", result.Metadata["isolated_workspace_apply_back"])
	}
	if got := result.Metadata["isolated_workspace_apply_back_enabled"]; got != true {
		t.Fatalf("isolated_workspace_apply_back_enabled = %v, want true", got)
	}
	if got := result.Metadata["isolated_workspace_review_required"]; got != true {
		t.Fatalf("isolated_workspace_review_required = %v, want true", got)
	}
	if got := result.Metadata["isolated_workspace_reviewed"]; got != true {
		t.Fatalf("isolated_workspace_reviewed = %v, want true", got)
	}
	if got := result.Metadata["isolated_workspace_review_files"]; got != 3 {
		t.Fatalf("isolated_workspace_review_files = %v, want 3", got)
	}
	if got := result.Metadata["isolated_workspace_strategy"]; got != "git_worktree" {
		t.Fatalf("isolated_workspace_strategy = %v, want git_worktree", got)
	}
	if got := result.Metadata["isolated_workspace_apply_back_mode"]; got == "" {
		t.Fatal("expected isolated_workspace_apply_back_mode metadata")
	}
	appliedFiles, ok := result.Metadata["isolated_workspace_applied_files"].([]string)
	if !ok {
		t.Fatalf("isolated_workspace_applied_files type = %T, want []string", result.Metadata["isolated_workspace_applied_files"])
	}
	if len(appliedFiles) != 3 {
		t.Fatalf("applied files = %v, want 3 entries", appliedFiles)
	}
	if result.Metadata["isolated_workspace_cleaned"] != true {
		t.Fatalf("isolated_workspace_cleaned = %v, want true", result.Metadata["isolated_workspace_cleaned"])
	}
	if _, err := os.Stat(agent.workDir); !os.IsNotExist(err) {
		t.Fatalf("expected isolated workspace to be removed, stat err = %v", err)
	}
	if reviewCalls != 1 {
		t.Fatalf("review calls = %d, want 1", reviewCalls)
	}
}

func TestMutatingGitAgentsCanRejectApplyBackReview(t *testing.T) {
	workDir := initGitRepo(t, map[string]string{
		"tracked.txt": "original\n",
	})
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(workDir))
	registry.MustRegister(tools.NewWriteTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	runner.SetWorkspaceIsolationEnabled(true)
	runner.SetWorkspaceReviewHandler(func(_ context.Context, changes []WorkspaceChangePreview) (bool, error) {
		if len(changes) != 1 {
			t.Fatalf("review changes = %d, want 1", len(changes))
		}
		if changes[0].FilePath != "tracked.txt" {
			t.Fatalf("review file = %q, want tracked.txt", changes[0].FilePath)
		}
		return false, nil
	})

	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic("writer", "Writes files", []string{"read", "write"}, "dynamic writer prompt"); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}
	runner.SetTypeRegistry(typeRegistry)

	agent := runner.newConfiguredAgent(context.Background(), runner.snapshotAgentDeps(), "writer", 3, "", nil)
	if agent.isolatedWorkspace == nil {
		t.Fatal("expected isolated workspace to be attached")
	}

	if err := os.WriteFile(filepath.Join(agent.workDir, "tracked.txt"), []byte("isolated workspace change\n"), 0644); err != nil {
		t.Fatalf("WriteFile(isolated tracked.txt): %v", err)
	}

	result := &AgentResult{
		AgentID:   agent.ID,
		Type:      agent.Type,
		Status:    AgentStatusCompleted,
		Completed: true,
	}
	err := runner.finalizeAgentWorkspace(agent, result)
	if err == nil {
		t.Fatal("expected review rejection to return an error")
	}
	if result.Status != AgentStatusFailed {
		t.Fatalf("status = %s, want %s", result.Status, AgentStatusFailed)
	}
	if !strings.Contains(result.Error, "rejected by user") {
		t.Fatalf("result error = %q, want rejection", result.Error)
	}
	if got := result.Metadata["isolated_workspace_review_rejected"]; got != true {
		t.Fatalf("isolated_workspace_review_rejected = %v, want true", got)
	}
	if got := result.Metadata["isolated_workspace_dir"]; got != agent.workDir {
		t.Fatalf("isolated_workspace_dir = %v, want %q", got, agent.workDir)
	}

	trackedContent, readErr := os.ReadFile(filepath.Join(workDir, "tracked.txt"))
	if readErr != nil {
		t.Fatalf("ReadFile(tracked.txt): %v", readErr)
	}
	if string(trackedContent) != "original\n" {
		t.Fatalf("tracked.txt = %q, want original", string(trackedContent))
	}
	if _, statErr := os.Stat(agent.workDir); statErr != nil {
		t.Fatalf("expected isolated workspace to remain for inspection: %v", statErr)
	}
}

func TestMutatingGitAgentsKeepWorkspaceOnApplyBackConflict(t *testing.T) {
	workDir := initGitRepo(t, map[string]string{
		"tracked.txt": "original\n",
	})
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(workDir))
	registry.MustRegister(tools.NewWriteTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	runner.SetWorkspaceIsolationEnabled(true)

	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic("writer", "Writes files", []string{"read", "write"}, "dynamic writer prompt"); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}
	runner.SetTypeRegistry(typeRegistry)

	agent := runner.newConfiguredAgent(context.Background(), runner.snapshotAgentDeps(), "writer", 3, "", nil)
	if agent.isolatedWorkspace == nil {
		t.Fatal("expected isolated workspace to be attached")
	}

	if err := os.WriteFile(filepath.Join(workDir, "tracked.txt"), []byte("main workspace change\n"), 0644); err != nil {
		t.Fatalf("WriteFile(main tracked.txt): %v", err)
	}
	if err := os.WriteFile(filepath.Join(agent.workDir, "tracked.txt"), []byte("isolated workspace change\n"), 0644); err != nil {
		t.Fatalf("WriteFile(isolated tracked.txt): %v", err)
	}

	result := &AgentResult{
		AgentID:   agent.ID,
		Type:      agent.Type,
		Status:    AgentStatusCompleted,
		Completed: true,
	}
	err := runner.finalizeAgentWorkspace(agent, result)
	if err == nil {
		t.Fatal("expected apply-back conflict to return an error")
	}
	if result.Status != AgentStatusFailed {
		t.Fatalf("status = %s, want %s", result.Status, AgentStatusFailed)
	}
	if !strings.Contains(result.Error, "failed to apply isolated workspace changes") {
		t.Fatalf("result error = %q, want apply-back failure", result.Error)
	}
	if result.Metadata["isolated_workspace_apply_back_error"] == nil {
		t.Fatal("expected isolated_workspace_apply_back_error metadata")
	}
	if got := result.Metadata["isolated_workspace_dir"]; got != agent.workDir {
		t.Fatalf("isolated_workspace_dir = %v, want %q", got, agent.workDir)
	}

	trackedContent, readErr := os.ReadFile(filepath.Join(workDir, "tracked.txt"))
	if readErr != nil {
		t.Fatalf("ReadFile(tracked.txt): %v", readErr)
	}
	if string(trackedContent) != "main workspace change\n" {
		t.Fatalf("tracked.txt = %q, want main workspace change", string(trackedContent))
	}
	if _, statErr := os.Stat(agent.workDir); statErr != nil {
		t.Fatalf("expected isolated workspace to remain for inspection: %v", statErr)
	}
}

func TestRecordAgentExecutionLearningRecordsAllTelemetry(t *testing.T) {
	strategyDir := testStrategyDir(t)
	promptDir := testStrategyDir(t)
	strategyOpt := NewStrategyOptimizer(strategyDir)
	promptOpt := NewPromptOptimizer(promptDir)
	exampleStore := &fakeExampleStore{calls: make(chan exampleLearnCall, 1)}

	runner := &Runner{}
	deps := runnerAgentDeps{
		strategyOptimizer: strategyOpt,
		exampleStore:      exampleStore,
		promptOptimizer:   promptOpt,
	}
	result := &AgentResult{
		Status: AgentStatusCompleted,
		Output: "analysis complete",
	}

	runner.recordAgentExecutionLearning(deps, "general", "inspect runner wiring", result, 2*time.Second, "spawn_async")

	metrics, ok := strategyOpt.GetMetrics("general")
	if !ok {
		t.Fatal("expected strategy metrics to be recorded")
	}
	if metrics.SuccessCount != 1 {
		t.Fatalf("success count = %d, want 1", metrics.SuccessCount)
	}
	if metrics.TaskTypes["spawn_async"] != 1 {
		t.Fatalf("task type count = %d, want 1", metrics.TaskTypes["spawn_async"])
	}

	variants := promptOpt.GetVariantsByBase("general")
	if len(variants) != 1 {
		t.Fatalf("variant count = %d, want 1", len(variants))
	}
	if variants[0].Variation != "inspect runner wiring" {
		t.Fatalf("prompt variation = %q, want inspect runner wiring", variants[0].Variation)
	}
	if variants[0].SuccessCount != 1 {
		t.Fatalf("variant success count = %d, want 1", variants[0].SuccessCount)
	}

	select {
	case call := <-exampleStore.calls:
		if call.taskType != "general" {
			t.Fatalf("task type = %q, want general", call.taskType)
		}
		if call.prompt != "inspect runner wiring" {
			t.Fatalf("prompt = %q, want inspect runner wiring", call.prompt)
		}
		if call.agentType != "general" {
			t.Fatalf("agent type = %q, want general", call.agentType)
		}
		if call.output != "analysis complete" {
			t.Fatalf("output = %q, want analysis complete", call.output)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for example-store learning")
	}
}
