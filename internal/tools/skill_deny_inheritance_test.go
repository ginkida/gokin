package tools

import "testing"

// A skill's `disallowed-tools` restriction must survive delegation. Before the
// clone inherited them, the model could escape any skill restriction simply by
// handing the same work to a sub-agent through the `task` tool.
func TestCloneToolForWorkDir_SkillDeniesTravelToSubAgents(t *testing.T) {
	workDir := t.TempDir()
	foreground := NewSkillTool(workDir)
	foreground.InheritPermissionDenies([]string{"bash", "write"})

	cloned, ok := CloneToolForWorkDir(foreground, t.TempDir()).(*SkillTool)
	if !ok {
		t.Fatal("clone did not return a *SkillTool")
	}
	assertDenies(t, cloned.ActivePermissionDenies(), "bash", "write")

	// The clone's own turn boundary must keep them (they are seeded as pending
	// too, exactly like a locally loaded skill's rules).
	cloned.BeginPermissionTurn()
	assertDenies(t, cloned.ActivePermissionDenies(), "bash", "write")

	// Grants must NOT travel: inheriting authority would be fail-open.
	if grants := cloned.ActivePermissionGrants(); len(grants) != 0 {
		t.Fatalf("clone inherited grants: %v", grants)
	}
}

func assertDenies(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("denies = %v, want %v", got, want)
	}
	set := make(map[string]struct{}, len(got))
	for _, deny := range got {
		set[deny] = struct{}{}
	}
	for _, deny := range want {
		if _, ok := set[deny]; !ok {
			t.Fatalf("denies = %v, missing %q", got, deny)
		}
	}
}
