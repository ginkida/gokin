package tools

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
		// A REPL kernel is session-scoped mutable state. Until sub-agents receive
		// their own runtime budgets and lifecycle owner, never share or clone the
		// foreground kernel into a delegated registry.
		if tool.Name() == "repl_exec" || tool.Name() == "harness" {
			continue
		}
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
		backgroundAllowed := t.backgroundAllowed
		workspaceBoundaryEnabled := t.workspaceBoundaryEnabled
		workspaceRoot := t.workspaceRoot
		taskManager := t.taskManager
		t.policyMu.RUnlock()

		dir := pickWorkDir(workDir, sourceWorkDir)
		cloned := NewBashTool(dir)
		cloned.SetTimeout(timeout)
		cloned.SetSandboxEnabled(sandboxEnabled)
		cloned.SetUnrestrictedMode(unrestrictedMode)
		cloned.SetBackgroundAllowed(backgroundAllowed)
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
		if taskManager != nil {
			// SHARE the foreground manager (v0.100.111). A per-clone
			// tasks.NewManager made a sub-agent's run_in_background task
			// write-only: invisible to /tasks, unreadable by task_output and
			// unstoppable by kill_shell (both are unclonable singletons bound
			// to the foreground manager), never cancelled at shutdown, and
			// its `task_<unixts>_<n>` ID could collide with another clone's.
			// The clone's working directory now travels with the task
			// (BashTool.executeBackground → Manager.StartInDir), so sharing
			// the manager does not move where the command runs.
			cloned.SetTaskManager(taskManager)
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
		cloned := NewGoToDefinitionTool(pickWorkDir(workDir, t.workDir))
		if workDir == "" {
			// Non-isolated agents share the foreground workspace and may safely
			// reuse its serialized managed-gopls provider. An explicit worktree
			// must never send paths to the foreground provider.
			cloned.SetSemanticProvider(t.semanticProvider())
		}
		return cloned
	case *FindReferencesTool:
		cloned := NewFindReferencesTool(pickWorkDir(workDir, t.workDir))
		if workDir == "" {
			cloned.SetSemanticProvider(t.semanticProvider())
		}
		return cloned
	case *GoSearchTool:
		cloned := NewGoSearchTool(pickWorkDir(workDir, t.workDir))
		if workDir == "" {
			cloned.SetSemanticProvider(t.semanticProvider())
		}
		return cloned
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
		cloned := NewPinContextTool(nil)
		cloned.SetWorkDir(pickWorkDir(workDir, t.persistenceWorkDir()))
		return cloned
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
		//
		// Skill DENIES travel with the clone: a `disallowed-tools` restriction
		// the active skill imposed must not be escapable by delegating the same
		// work to a sub-agent. Grants deliberately do NOT travel — inheriting
		// authority would be fail-open.
		if workDir != "" {
			cloned := NewSkillTool(workDir)
			cloned.SetWorkspaceTrusted(t.WorkspaceTrusted())
			cloned.InheritPermissionDenies(t.ActivePermissionDenies())
			return cloned
		}
		cloned := NewSkillToolWithCatalogAndWorkDir(t.catalog, t.workDir)
		cloned.SetWorkspaceTrusted(t.WorkspaceTrusted())
		cloned.InheritPermissionDenies(t.ActivePermissionDenies())
		return cloned
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
	case *ReplExecTool:
		// The REPL kernel is a single Python subprocess bound to the foreground
		// workspace, and Manager.Execute serializes every cell behind one mutex.
		// Sharing the foreground instance would leak globals between agents, let
		// one agent's action=reset wipe another's mid-analysis state, and let a
		// long cell block everyone else's REPL work. A clone therefore gets NO
		// manager, which degrades honestly ("stateful REPL is unavailable in
		// this session") instead of reaching into the foreground kernel.
		//
		// Sub-agents do not receive the tool at all (agent.foregroundOnlyTools);
		// this is the second layer, for any path that hands it over anyway.
		return NewReplExecTool(nil)
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
