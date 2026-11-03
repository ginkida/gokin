package app

import (
	"reflect"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestSessionRunPolicyRejectsRuntimeSwitch(t *testing.T) {
	for _, onTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "target"}[onTarget], func(t *testing.T) {
			t.Setenv("HOME", testkit.ResolvedTempDir(t))
			t.Setenv("XDG_DATA_HOME", testkit.ResolvedTempDir(t))
			workDir := testkit.ResolvedTempDir(t)
			a := newSessionLeaseTestApp(t, "policy-current", "glm", workDir, "current")
			target := saveSessionLeaseFixture(t, "policy-target", "glm", workDir, "target")
			policy := &chat.SessionRunPolicy{ToolCeiling: []string{}, PermissionMode: "plan"}
			if onTarget {
				s := chat.NewSession()
				if err := s.RestoreFromState(target); err != nil {
					t.Fatal(err)
				}
				s.SetRunPolicy(policy)
				hm, err := chat.NewHistoryManager()
				if err != nil {
					t.Fatal(err)
				}
				if err := hm.SaveFull(s); err != nil {
					t.Fatal(err)
				}
				// Candidate policy is deliberately stale: enforce the leased reload.
			} else {
				a.session.SetRunPolicy(policy)
			}
			before := a.session.GetID()
			if _, err := a.SwitchSession(t.Context(), target, true); err == nil || !strings.Contains(err.Error(), "--resume") {
				t.Fatalf("runtime policy switch should require CLI restart: %v", err)
			}
			if a.session.GetID() != before {
				t.Fatal("refused switch changed session")
			}
		})
	}
}

func TestSessionRunPolicySurvivesClearRotation(t *testing.T) {
	t.Setenv("HOME", testkit.ResolvedTempDir(t))
	a := newRewindApp(t)
	policy := &chat.SessionRunPolicy{ToolCeiling: []string{}, DeniedRules: []string{"bash(git push*)"}, PermissionMode: "plan"}
	a.session.SetRunPolicy(policy)
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := a.session.RunPolicy(); !reflect.DeepEqual(got, policy) {
		t.Fatalf("clear discarded authority: %+v", got)
	}
}

func TestSessionRunPolicySurvivesConversationRewind(t *testing.T) {
	t.Setenv("HOME", testkit.ResolvedTempDir(t))
	a := newRewindApp(t)
	a.captureTurnRewind("old unrestricted turn")
	a.session.AddUserMessage("old unrestricted turn")
	points, err := a.ListRewindPoints(t.Context())
	if err != nil || len(points) != 1 {
		t.Fatalf("points=%v err=%v", points, err)
	}
	policy := &chat.SessionRunPolicy{ToolCeiling: []string{}, PermissionMode: "plan"}
	a.session.SetRunPolicy(policy)
	if _, err := a.RewindToPoint(t.Context(), points[0].ID, "conversation"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.session.RunPolicy(), policy) {
		t.Fatal("rewind restored older, wider authority")
	}
}

func TestSessionRunPolicyEmptyCeilingSurvivesRegistryRefresh(t *testing.T) {
	a := &App{registry: tools.NewRegistry()}
	a.registry.MustRegister(&appHeadlessScriptedTool{name: "read"})
	if err := a.ConfigureToolCapability([]string{}, nil); err != nil {
		t.Fatal(err)
	}
	a.refreshToolCapabilityCeiling()
	ceiling, restricted := a.toolCapabilitySnapshot()
	if !restricted || ceiling == nil || len(ceiling) != 0 {
		t.Fatalf("empty ceiling widened after registry refresh: %#v restricted=%v", ceiling, restricted)
	}
}
