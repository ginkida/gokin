package chat

import "gokin/internal/goals"

func (s *Session) GoalState() goals.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return goals.Clone(s.goalState)
}

func (s *Session) SetGoalState(state goals.State) error {
	if err := goals.Validate(state); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goalState = goals.Clone(state)
	return nil
}
