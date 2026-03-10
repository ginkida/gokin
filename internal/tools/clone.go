package tools

import "gokin/internal/tasks"

// CloneRegistryForWorkDir clones a registry for a different workspace root.
// Tools with workspace-bound state get fresh instances pointed at workDir.
func CloneRegistryForWorkDir(baseRegistry ToolRegistry, workDir string) *Registry {
	cloned := NewRegistry()
	hasToolsList := false

	for _, tool := range baseRegistry.List() {
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
		return NewWriteTool(pickWorkDir(workDir, t.workDir))
	case *EditTool:
		return NewEditTool(pickWorkDir(workDir, t.workDir))
	case *BashTool:
		dir := pickWorkDir(workDir, t.workDir)
		cloned := NewBashTool(dir)
		cloned.timeout = t.timeout
		cloned.sandboxEnabled = t.sandboxEnabled
		cloned.unrestrictedMode = t.unrestrictedMode
		cloned.SetWorkspaceBoundary(dir)
		if t.ManagedWorkspaceApplyBackModeEnabled() || (workDir != "" && dir != t.workDir) {
			cloned.EnableManagedWorkspaceApplyBackMode(dir)
		}
		if t.taskManager != nil {
			cloned.SetTaskManager(tasks.NewManager(dir))
		}
		return cloned
	case *GlobTool:
		return NewGlobTool(pickWorkDir(workDir, t.workDir))
	case *GrepTool:
		return NewGrepTool(pickWorkDir(workDir, t.workDir))
	case *ListDirTool:
		return NewListDirTool(pickWorkDir(workDir, t.baseDir))
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
	case *CodeGraphTool:
		cloned := NewCodeGraphTool()
		cloned.SetWorkDir(pickWorkDir(workDir, t.workDir))
		return cloned
	case *CopyTool:
		return NewCopyTool(pickWorkDir(workDir, t.workDir))
	case *MoveTool:
		return NewMoveTool(pickWorkDir(workDir, t.workDir))
	case *DeleteTool:
		return NewDeleteTool(pickWorkDir(workDir, t.workDir))
	case *MkdirTool:
		return NewMkdirTool(pickWorkDir(workDir, t.workDir))
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
	case *SemanticSearchTool:
		return NewSemanticSearchTool(t.indexer, pickWorkDir(workDir, t.workDir), t.topK)
	case *RequestToolTool:
		return NewRequestToolTool()
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
	case *ToolsListTool:
		// Registry-aware cloning is handled by CloneRegistryForWorkDir.
		return NewToolsListTool(nil)
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
