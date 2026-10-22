package chat

import (
	"reflect"
	"testing"
	"time"

	"gokin/internal/goals"
)

func TestGoalSurvivesSaveResumeAndForkButClearRemovesIt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := NewSession()
	state, err := goals.Set(goals.State{}, "all acceptance criteria hold", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetGoalState(state); err != nil {
		t.Fatal(err)
	}
	manager, err := NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveFull(session); err != nil {
		t.Fatal(err)
	}
	saved, err := manager.LoadFull(session.GetID())
	if err != nil {
		t.Fatal(err)
	}
	resumed := NewSession()
	if err := resumed.RestoreFromState(saved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumed.GoalState(), state) {
		t.Fatal("resume lost the goal")
	}
	branch := session.Fork("alternate")
	copy := branch.GoalState()
	copy.Current.Condition = "mutated"
	if session.GoalState().Current.Condition == "mutated" || branch.GoalState().Current.Condition == "mutated" {
		t.Fatal("goal snapshot aliases another owner")
	}
	session.Clear()
	if session.GoalState().Current != nil || branch.GoalState().Current == nil {
		t.Fatal("clear crossed session ownership")
	}
}

func TestInvalidGoalCannotPartiallyRestoreSession(t *testing.T) {
	session := NewSession()
	session.AddUserMessage("original")
	state := session.GetState()
	state.ID = "different"
	state.Goal = goals.State{Current: &goals.Goal{ID: "bad", Condition: "do work", Status: goals.Active}}
	if err := session.RestoreFromState(state); err == nil || session.GetID() == "different" {
		t.Fatal("invalid goal partially replaced the conversation", err)
	}
}
