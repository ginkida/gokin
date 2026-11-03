package permission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

func TestSensitiveReadFloor(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithWorkDir(context.Background(), work)
	for _, enabled := range []bool{true, false} {
		for _, tc := range []struct {
			name string
			args map[string]any
		}{
			{"read", map[string]any{"file_path": ".env"}},
			{"diff", map[string]any{"file1": "ordinary", "file2": ".env"}},
			{"git_diff", map[string]any{"file": ".env"}},
			{"git_blame", map[string]any{"file": ".env"}},
			{"lsp", map[string]any{"filePath": ".env"}},
			{"go_to_definition", map[string]any{"file": ".env"}},
			{"find_references", map[string]any{"file": ".env"}},
			{"grep", map[string]any{"path": ".env", "pattern": "fixture"}},
		} {
			t.Run(tc.name+map[bool]string{true: "/allow", false: "/bypass"}[enabled], func(t *testing.T) {
				rules := DefaultRules()
				rules.SetPolicy(tc.name, LevelAllow)
				m := NewManager(rules, enabled)
				response, _ := m.Check(ctx, tc.name, tc.args)
				if response.Allowed || !strings.Contains(response.Reason, "--allowed-tools 'Read(./.env)'") {
					t.Fatalf("credential read: %+v", response)
				}
			})
		}
	}
}

func TestSensitiveReadExplicitApproval(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	ctx := ContextWithWorkDir(context.Background(), work)
	for _, rule := range []string{"", "Read", "Read(./other)", "Read(./.env)"} {
		t.Run(rule, func(t *testing.T) {
			m := NewManager(nil, true)
			if rule != "" {
				rules, err := ParseTemporaryToolGrantList(rule)
				if err != nil {
					t.Fatal(err)
				}
				m.SetRunToolRules(rules, nil)
			}
			calls := 0
			m.SetPromptHandler(func(context.Context, *Request) (Decision, error) { calls++; return DecisionAllowSession, nil })
			for _, offset := range []int{0, 10} {
				r, err := m.Check(ctx, "read", map[string]any{"file_path": ".env", "offset": offset})
				if err != nil || !r.Allowed {
					t.Fatalf("approval: %+v %v", r, err)
				}
			}
			want := 1
			if rule == "Read(./.env)" {
				want = 0
			}
			if calls != want {
				t.Fatalf("prompts=%d want=%d", calls, want)
			}
			r, _ := m.Check(ctx, "read", map[string]any{"file_path": ".env.local"})
			if !r.Allowed || calls != want+1 {
				t.Fatalf("approval leaked to another path: %+v prompts=%d", r, calls)
			}
		})
	}
}

func TestSensitiveReadBoundaries(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	ctx := ContextWithWorkDir(context.Background(), work)
	if err := os.Mkdir(filepath.Join(work, ".env"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"grep", map[string]any{"path": ".env"}},
		{"read", map[string]any{"file_path": ".env.example"}},
		{"write", map[string]any{"file_path": "credentials.json"}},
		{"edit", map[string]any{"file_path": "credentials.json"}},
	} {
		rules := DefaultRules()
		rules.SetPolicy(tc.name, LevelAllow)
		m := NewManager(rules, true)
		if r, err := m.Check(ctx, tc.name, tc.args); err != nil || !r.Allowed {
			t.Errorf("non-file/non-read unexpectedly gated: %+v %v", r, err)
		}
	}
	if err := os.Symlink(filepath.Join(work, "private.key"), filepath.Join(work, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "private.key"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(nil, true)
	if r, _ := m.Check(ctx, "read", map[string]any{"file_path": "alias"}); r.Allowed {
		t.Error("symlink alias bypassed sensitive classification")
	}
	rules := DefaultRules()
	rules.SetPolicy("read", LevelDeny)
	m = NewManager(rules, true)
	allows, _ := ParseTemporaryToolGrantList("Read(./private.key)")
	m.SetRunToolRules(allows, nil)
	if r, _ := m.Check(ctx, "read", map[string]any{"file_path": "private.key"}); r.Allowed {
		t.Error("path preapproval overrode config deny")
	}
	m = NewManager(nil, true)
	m.SetDontAsk(true)
	if r, _ := m.Check(ctx, "read", map[string]any{"file_path": "private.key"}); r.Allowed || !strings.Contains(r.Reason, "--allowed-tools") {
		t.Errorf("dontAsk floor: %+v", r)
	}
}
