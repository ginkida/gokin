package main

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/testkit"

	"github.com/google/uuid"
)

func TestEffectiveSessionRunPolicy(t *testing.T) {
	stored := &chat.SessionRunPolicy{ToolCeiling: []string{"read", "grep"}, DeniedRules: []string{"bash(git push*)"}, PermissionMode: "default"}
	for _, tc := range []struct {
		name                    string
		stored, requested, want *chat.SessionRunPolicy
		reset, narrowed         bool
	}{
		{"omitted tools", stored, &chat.SessionRunPolicy{PermissionMode: "bypassPermissions"}, stored, false, true},
		{"intersection", stored, &chat.SessionRunPolicy{ToolCeiling: []string{"read", "write"}, DeniedRules: []string{"Write"}, PermissionMode: "acceptEdits"}, &chat.SessionRunPolicy{ToolCeiling: []string{"read"}, DeniedRules: []string{"bash(git push*)", "write"}, PermissionMode: "default"}, false, true},
		{"empty", &chat.SessionRunPolicy{ToolCeiling: []string{}, PermissionMode: "plan"}, &chat.SessionRunPolicy{ToolCeiling: []string{"read"}, PermissionMode: "default"}, &chat.SessionRunPolicy{ToolCeiling: []string{}, PermissionMode: "plan"}, false, true},
		{"new narrower", &chat.SessionRunPolicy{PermissionMode: "bypassPermissions"}, &chat.SessionRunPolicy{ToolCeiling: []string{"read"}, PermissionMode: "plan"}, &chat.SessionRunPolicy{ToolCeiling: []string{"read"}, PermissionMode: "plan"}, false, false},
		{"dontAsk retained", &chat.SessionRunPolicy{PermissionMode: "dontAsk"}, &chat.SessionRunPolicy{PermissionMode: "default"}, &chat.SessionRunPolicy{PermissionMode: "dontAsk"}, false, true},
		{"reset widens", stored, &chat.SessionRunPolicy{PermissionMode: "bypassPermissions"}, &chat.SessionRunPolicy{PermissionMode: "bypassPermissions"}, true, false},
		{"legacy", nil, &chat.SessionRunPolicy{PermissionMode: "bypassPermissions"}, &chat.SessionRunPolicy{PermissionMode: "bypassPermissions"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, narrowed, err := effectiveSessionRunPolicy(tc.stored, tc.requested, tc.reset)
			if err != nil || narrowed != tc.narrowed || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, narrowed=%v err=%v; want %+v, narrowed=%v", got, narrowed, err, tc.want, tc.narrowed)
			}
		})
	}
	if _, _, err := effectiveSessionRunPolicy(&chat.SessionRunPolicy{PermissionMode: "typo"}, stored, false); err == nil {
		t.Fatal("unknown persisted mode must fail closed")
	}
	for storedRank, storedMode := range []string{"plan", "default", "acceptEdits", "bypassPermissions"} {
		for requestedRank, requestedMode := range []string{"plan", "default", "acceptEdits", "bypassPermissions"} {
			got, _, err := effectiveSessionRunPolicy(&chat.SessionRunPolicy{PermissionMode: storedMode}, &chat.SessionRunPolicy{PermissionMode: requestedMode}, false)
			want := requestedMode
			if storedRank < requestedRank {
				want = storedMode
			}
			if err != nil || got.PermissionMode != want {
				t.Fatalf("%s + %s = %+v (%v), want %s", storedMode, requestedMode, got, err, want)
			}
		}
	}
}

func TestRunPolicyBackgroundPreflight(t *testing.T) {
	t.Setenv("HOME", testkit.ResolvedTempDir(t))
	t.Setenv("XDG_DATA_HOME", testkit.ResolvedTempDir(t))
	t.Cleanup(func() { newRootCommand() })
	workDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession()
	session.SetID(uuid.NewString())
	session.SetWorkDir(workDir)
	session.SetRunPolicy(&chat.SessionRunPolicy{ToolCeiling: []string{"read"}, PermissionMode: "unknown"})
	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := history.SaveFull(session); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args    []string
		wantErr bool
	}{
		{[]string{"--tools", "@unknown"}, true},
		{[]string{"--resume", session.GetID()}, true},
		{[]string{"--resume", session.GetID(), "--reset-session-policy", "--tools", "@read"}, false},
	} {
		cmd := newRootCommand()
		if err := cmd.ParseFlags(tc.args); err != nil {
			t.Fatal(err)
		}
		if err := preflightBackgroundLaunch(cmd); (err != nil) != tc.wantErr {
			t.Fatalf("flags %v: error %v, want error=%v", tc.args, err, tc.wantErr)
		}
	}
}

func TestWarnScopedGitDenies(t *testing.T) {
	for _, tc := range []struct{ rule, tool string }{
		{"bash(git commit*)", "git_commit"}, {"bash(git push*)", "git_pr"},
		{"bash(git checkout -b*)", "git_branch"}, {"bash(git switch -c*)", "git_branch"},
		{"bash(git add*)", "git_add"},
	} {
		t.Run(tc.tool+tc.rule, func(t *testing.T) {
			p := &chat.SessionRunPolicy{DeniedRules: []string{tc.rule}}
			var out bytes.Buffer
			warnScopedGitDenies(&out, []string{tc.tool}, p)
			if !strings.Contains(out.String(), tc.rule+" does not restrict the "+tc.tool+" tool; add --disallowed-tools "+tc.tool) {
				t.Fatalf("warning missing: %q", out.String())
			}
			for _, blocked := range []*chat.SessionRunPolicy{
				{ToolCeiling: []string{}, DeniedRules: p.DeniedRules},
				{DeniedRules: []string{tc.rule, tc.tool}},
				{DeniedRules: []string{tc.rule, "git_*"}},
			} {
				out.Reset()
				warnScopedGitDenies(&out, []string{tc.tool}, blocked)
				if out.Len() != 0 {
					t.Fatalf("warned for blocked dedicated tool: %s", &out)
				}
			}
		})
	}
}
