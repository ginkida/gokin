package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalizeAgentWorkspaceHonorsPerFileReview(t *testing.T) {
	base := initGitRepo(t, map[string]string{
		"approved.txt": "old approved\n",
		"rejected.txt": "old rejected\n",
	})
	workspace, err := prepareGitWorktree(base, true)
	if err != nil {
		t.Fatalf("prepareGitWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Root, "approved.txt"), []byte("new approved\n"), 0644); err != nil {
		t.Fatalf("write approved change: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Root, "rejected.txt"), []byte("new rejected\n"), 0644); err != nil {
		t.Fatalf("write rejected change: %v", err)
	}

	runner := NewRunner(context.Background(), nil, nil, base)
	runner.SetWorkspaceReviewHandler(func(_ context.Context, previews []WorkspaceChangePreview) ([]string, error) {
		if len(previews) != 2 {
			t.Fatalf("review previews = %d, want 2", len(previews))
		}
		return []string{"approved.txt"}, nil
	})
	agent := &Agent{ID: "selective", Type: "writer", workDir: workspace.Root, isolatedWorkspace: workspace}
	result := &AgentResult{AgentID: agent.ID, Type: agent.Type, Status: AgentStatusCompleted, Completed: true}
	if err := runner.finalizeAgentWorkspace(agent, result); err != nil {
		t.Fatalf("finalizeAgentWorkspace: %v", err)
	}

	assertFileContent(t, filepath.Join(base, "approved.txt"), "new approved\n")
	assertFileContent(t, filepath.Join(base, "rejected.txt"), "old rejected\n")
	if got := result.Metadata["isolated_workspace_review_approved_files"]; got != 1 {
		t.Fatalf("approved metadata = %v, want 1", got)
	}
	if got := result.Metadata["isolated_workspace_review_rejected_files"]; got != 1 {
		t.Fatalf("rejected metadata = %v, want 1", got)
	}
}

func TestIsolatedWorkspaceApplyBackFilesAppliesOnlyReviewedSubset(t *testing.T) {
	base := initGitRepo(t, map[string]string{
		"approved.txt": "old approved\n",
		"rejected.txt": "old rejected\n",
	})
	workspace, err := prepareGitWorktree(base, true)
	if err != nil {
		t.Fatalf("prepareGitWorktree: %v", err)
	}
	defer func() { _ = workspace.Cleanup() }()

	if err := os.WriteFile(filepath.Join(workspace.Root, "approved.txt"), []byte("new approved\n"), 0644); err != nil {
		t.Fatalf("write approved change: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Root, "rejected.txt"), []byte("new rejected\n"), 0644); err != nil {
		t.Fatalf("write rejected change: %v", err)
	}

	result, err := workspace.ApplyBackFiles([]string{"approved.txt"})
	if err != nil {
		t.Fatalf("ApplyBackFiles: %v", err)
	}
	if len(result.ChangedFiles) != 1 || result.ChangedFiles[0] != "approved.txt" {
		t.Fatalf("applied files = %v, want [approved.txt]", result.ChangedFiles)
	}
	assertFileContent(t, filepath.Join(base, "approved.txt"), "new approved\n")
	assertFileContent(t, filepath.Join(base, "rejected.txt"), "old rejected\n")
}

func TestValidateReviewedWorkspaceFilesRejectsUnreviewedPaths(t *testing.T) {
	previews := []WorkspaceChangePreview{{FilePath: "reviewed.txt"}}
	if _, err := validateReviewedWorkspaceFiles(previews, []string{"../outside.txt"}); err == nil {
		t.Fatal("path traversal selection was accepted")
	}
	if _, err := validateReviewedWorkspaceFiles(previews, []string{"other.txt"}); err == nil {
		t.Fatal("unreviewed selection was accepted")
	}
	spaced := " reviewed file.txt "
	got, err := validateReviewedWorkspaceFiles([]WorkspaceChangePreview{{FilePath: spaced}}, []string{spaced})
	if err != nil || len(got) != 1 || got[0] != spaced {
		t.Fatalf("valid spaced filename was changed or rejected: got=%q err=%v", got, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
