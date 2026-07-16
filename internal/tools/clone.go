package tools

import "gokin/internal/tasks"

// CloneRegistryForWorkDir clones a registry for a different workspace root.
// Tools with workspace-bound state get fresh instances pointed at workDir.
func CloneRegistryForWorkDir(baseRegistry ToolRegistry, workDir string) *Registry {
	return CloneRegistryForWorkDirWithToolCeiling(baseRegistry, workDir, nil)
}

// CloneRegistryForWorkDirWithToolCeiling clones only tools authorized by the
// caller. A nil ceiling preserves the full registry; a non-nil empty ceiling
// deliberately produces an empty registry.
func CloneRegistryForWorkDirWithToolCeiling(baseRegistry ToolRegistry, workDir string, ceiling []string) *Registry {
	cloned := NewRegistry()
	hasToolsList := false
	restricted := ceiling != nil
	allowed := make(map[string]struct{}, len(ceiling))
	for _, name := range ceiling {
		allowed[name] = struct{}{}
	}

	for _, tool := range baseRegistry.List() {
		if restricted {
			if _, ok := allowed[tool.Name()]; !ok {
				continue
			}
		}
		if tool.Name() == "tools_list" {
			hasToolsList = true
			continue
		}
		_ = cloned.Register(CloneToolForWorkDir(tool, workDir))
	}

	if hasToolsList {
		_ = cloned.Register(NewToolsListTool(cloned))
	}

	return cloned
}

// CloneToolForWorkDir clones a tool for agent-local use. If workDir is empty,
// the tool keeps its current workspace binding.
func CloneToolForWorkDir(tool Tool, workDir string) Tool {
	switch t := tool.(type) {
	case *ReadTool:
		cloned := NewReadTool(pickWorkDir(workDir, t.workDir))
		cloned.predictor = t.predictor
		return cloned
	case *WriteTool:
		cloned := NewWriteTool(pickWorkDir(workDir, t.workDir))
		cloned.undoManager = t.undoManager
		return cloned
	case *EditTool:
		cloned := NewEditTool(pickWorkDir(workDir, t.workDir))
		cloned.undoManager = t.undoManager
		return cloned
	case *BashTool:
		t.policyMu.RLock()
		sourceWorkDir := t.workDir
		timeout := t.timeout
		sandboxEnabled := t.sandboxEnabled
		unrestrictedMode := t.unrestrictedMode
		managedApplyBack := t.managedWorkspaceApplyBack
		workspaceBoundaryEnabled := t.workspaceBoundaryEnabled
		workspaceRoot := t.workspaceRoot
		hasTaskManager := t.taskManager != nil
		t.policyMu.RUnlock()

		dir := pickWorkDir(workDir, sourceWorkDir)
		cloned := NewBashTool(dir)
		cloned.SetTimeout(timeout)
		cloned.SetSandboxEnabled(sandboxEnabled)
		cloned.SetUnrestrictedMode(unrestrictedMode)
		if workDir != "" {
			// An explicit agent workspace is a new security boundary, regardless
			// of the foreground shell's current boundary.
			cloned.SetWorkspaceBoundary(dir)
		} else if workspaceBoundaryEnabled {
			// With no override this is a true policy clone. Do not silently widen
			// a narrower source boundary back to its original construction dir.
			cloned.SetWorkspaceBoundary(workspaceRoot)
		}
		if managedApplyBack {
			managedRoot := workspaceRoot
			if workDir != "" || managedRoot == "" {
				managedRoot = dir
			}
			cloned.EnableManagedWorkspaceApplyBackMode(managedRoot)
		} else if workDir != "" && dir != sourceWorkDir {
			cloned.EnableManagedWorkspaceApplyBackMode(dir)
		}
		if hasTaskManager {
			cloned.SetTaskManager(tasks.NewManager(dir))
		}
		return cloned
	case *GlobTool:
		return NewGlobTool(pickWorkDir(workDir, t.workDir))
	case *GrepTool:
		return NewGrepTool(pickWorkDir(workDir, t.workDir))
	case *ListDirTool:
		return NewListDirTool(pickWorkDir(workDir, t.baseDir))
	case *DiffTool:
		return NewDiffTool(pickWorkDir(workDir, t.workDir))
	case *TreeTool:
		return NewTreeTool(pickWorkDir(workDir, t.workDir))
	case *BatchTool:
		cloned := NewBatchTool(pickWorkDir(workDir, t.workDir))
		cloned.undoManager = t.undoManager
		cloned.progressCallback = t.progressCallback
		cloned.failureThreshold = t.failureThreshold
		return cloned
	case *RefactorTool:
		cloned := NewRefactorTool()
		cloned.SetWorkDir(pickWorkDir(workDir, t.workDir))
		cloned.undoManager = t.undoManager
		cloned.diffHandler = t.diffHandler
		cloned.diffEnabled = t.diffEnabled
		return cloned
	case *CopyTool:
		cloned := NewCopyTool(pickWorkDir(workDir, t.workDir))
		cloned.undoManager = t.undoManager
		return cloned
	case *MoveTool:
		cloned := NewMoveTool(pickWorkDir(workDir, t.workDir))
		cloned.undoManager = t.undoManager
		return cloned
	case *DeleteTool:
		cloned := NewDeleteTool(pickWorkDir(workDir, t.workDir))
		cloned.undoManager = t.undoManager
		return cloned
	case *MkdirTool:
		cloned := NewMkdirTool(pickWorkDir(workDir, t.workDir))
		cloned.undoManager = t.undoManager
		return cloned
	case *GitLogTool:
		return NewGitLogTool(pickWorkDir(workDir, t.workDir))
	case *GitBlameTool:
		return NewGitBlameTool(pickWorkDir(workDir, t.workDir))
	case *GitDiffTool:
		return NewGitDiffTool(pickWorkDir(workDir, t.workDir))
	case *GitStatusTool:
		return NewGitStatusTool(pickWorkDir(workDir, t.workDir))
	case *GitAddTool:
		return NewGitAddTool(pickWorkDir(workDir, t.workDir))
	case *GitCommitTool:
		return NewGitCommitTool(pickWorkDir(workDir, t.workDir))
	case *GitBranchTool:
		return NewGitBranchTool(pickWorkDir(workDir, t.workDir))
	case *GitPRTool:
		return NewGitPRTool(pickWorkDir(workDir, t.workDir))
	case *RunTestsTool:
		return NewRunTestsTool(pickWorkDir(workDir, t.workDir))
	case *VerifyCodeTool:
		return NewVerifyCodeTool(pickWorkDir(workDir, t.workDir))
	case *CheckImpactTool:
		return NewCheckImpactTool(pickWorkDir(workDir, t.workDir))
	case *GoToDefinitionTool:
		// Carries workDir + a per-agent SetAllowedDirs mutator (rebuilds its
		// PathValidator). Without a clone case every sub-agent shared the
		// foreground instance, so a worktree-isolated agent's SetGrantedDirs
		// clobbered the shared pathValidator (and multiple isolated agents raced
		// it). Fresh per-agent instance; the runner re-applies grants after clone.
		return NewGoToDefinitionTool(pickWorkDir(workDir, t.workDir))
	case *FindReferencesTool:
		return NewFindReferencesTool(pickWorkDir(workDir, t.workDir))
	case *GoSearchTool:
		return NewGoSearchTool(pickWorkDir(workDir, t.workDir))
	case *ReviewChangesTool:
		// Carries workDir (git commands run with cmd.Dir = workDir). Without a
		// clone case a worktree-isolated agent's self-review ran `git diff`
		// against the FOREGROUND repo, not its own worktree.
		return NewReviewChangesTool(pickWorkDir(workDir, t.workDir))
	case *RequestToolTool:
		return NewRequestToolTool()
	case *TaskTool:
		// Keep the live runner/catalog contract, but never share the mutable
		// parent-local capability ceiling between agent instances.
		return t.clone()
	case *AskAgentTool:
		return NewAskAgentTool()
	case *PinContextTool:
		return NewPinContextTool(nil)
	case *HistorySearchTool:
		return NewHistorySearchTool(nil)
	case *UpdateScratchpadTool:
		return NewUpdateScratchpadTool(nil)
	case *SharedMemoryTool:
		cloned := NewSharedMemoryTool()
		cloned.SetMemory(t.GetMemory())
		return cloned
	case *MemorizeTool:
		return NewMemorizeTool(t.GetLearning())
	case *MemoryTool:
		// Give each agent its OWN MemoryTool instance. The runner/agent re-wires
		// SetLearning(pl) per-agent after construction, so a SHARED instance would
		// race on that field under parallel (non-isolated) spawn AND let an
		// isolated worktree agent clobber the foreground's learning pointer with
		// its own worktree-scoped store. The kv-store (*memory.Store) is itself
		// concurrency-safe, so sharing that pointer is fine. Same footgun the
		// *TodoTool / *MemorizeTool cases already guard against.
		cloned := NewMemoryTool()
		cloned.SetStore(t.store)
		cloned.SetLearning(t.learning)
		// Share the live global-scope policy (the pointer contains an atomic), so
		// revoking memory.allow_global also reaches agents cloned before the
		// config change instead of leaving them with stale cross-project access.
		cloned.allowGlobal = t.allowGlobal
		return cloned
	case *SkillTool:
		// Project skills are workspace-bound instructions. An isolated agent
		// must discover them from its worktree rather than retaining the
		// foreground catalog. With no override, preserve the existing binding
		// while still returning an agent-local tool instance; Catalog itself is
		// immutable-by-snapshot and safe to share across concurrent callers.
		if workDir != "" {
			return NewSkillTool(workDir)
		}
		return NewSkillToolWithCatalogAndWorkDir(t.catalog, t.workDir)
	case *ToolsListTool:
		// Registry-aware cloning is handled by CloneRegistryForWorkDir.
		return NewToolsListTool(nil)
	case *TodoTool:
		// Give each agent its OWN todo list. Sharing the foreground instance is a
		// footgun: the todo tool REPLACES the full list on every call, so a
		// sub-agent's todo update would clobber the foreground's task list — and
		// it lets the agent loop's incomplete-work continuation read foreign
		// todos. A fresh list keeps each agent's progress its own.
		return NewTodoTool()
	default:
		return tool
	}
}

func pickWorkDir(override string, current string) string {
	if override != "" {
		return override
	}
	return current
}
