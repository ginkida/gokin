package chat

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSessionRunPolicyRoundTrip(t *testing.T) {
	for _, ceiling := range [][]string{nil, {}, {"read", "grep"}} {
		s := NewSession()
		want := &SessionRunPolicy{ToolCeiling: ceiling, DeniedRules: []string{"bash(git push*)"}, PermissionMode: "default"}
		s.SetRunPolicy(want)
		encoded, err := json.Marshal(s.GetState())
		if err != nil {
			t.Fatal(err)
		}
		var state SessionState
		if err := json.Unmarshal(encoded, &state); err != nil {
			t.Fatal(err)
		}
		restored := NewSession()
		if err := restored.RestoreFromState(&state); err != nil {
			t.Fatal(err)
		}
		if got := restored.RunPolicy(); !reflect.DeepEqual(got, want) {
			t.Fatalf("policy = %#v, want %#v; JSON %s", got, want, encoded)
		}
	}
	legacy := NewSession()
	encoded, err := json.Marshal(legacy.GetState())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["run_policy"]; ok {
		t.Fatal("legacy policy must remain absent")
	}
}

func TestSessionRunPolicyOwnershipClearAndFork(t *testing.T) {
	s := NewSession()
	input := &SessionRunPolicy{ToolCeiling: []string{"read"}, DeniedRules: []string{"write"}, PermissionMode: "plan"}
	s.SetRunPolicy(input)
	input.ToolCeiling[0] = "bash"
	snapshot := s.RunPolicy()
	snapshot.DeniedRules[0] = "read"
	s.Clear()
	for _, fork := range []bool{false, true} {
		state := s.GetState()
		if fork {
			var err error
			state, err = PrepareSessionFork(state, "policy-fork", time.Now())
			if err != nil {
				t.Fatal(err)
			}
		}
		if state.RunPolicy == nil || !reflect.DeepEqual(state.RunPolicy.ToolCeiling, []string{"read"}) ||
			!reflect.DeepEqual(state.RunPolicy.DeniedRules, []string{"write"}) {
			t.Fatalf("snapshot/fork lost or aliased policy: %+v", state.RunPolicy)
		}
		state.RunPolicy.ToolCeiling[0] = "edit"
	}
	branch := s.Fork("branch")
	if !reflect.DeepEqual(branch.RunPolicy(), s.RunPolicy()) {
		t.Fatal("branch did not inherit policy")
	}
	branch.SetRunPolicy(nil)
	if s.RunPolicy() == nil {
		t.Fatal("branch changed parent policy")
	}
	if err := s.RestoreFromState(NewSession().GetState()); err != nil || s.RunPolicy() != nil {
		t.Fatal("legacy restore retained a different session's policy", err)
	}
}
