package chat

// ContextMemory returns the session-owned snapshots together with their owner.
// Workspace-wide markdown files cannot establish which conversation owns them.
func (s *Session) ContextMemory() (id, working, summary string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ID, s.workingMemory, s.sessionMemory
}

func (s *Session) SetWorkingMemory(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workingMemory != content {
		s.workingMemory = content
		s.version++
	}
}

// SetSessionMemory accepts asynchronous extraction only for its owning session.
func (s *Session) SetSessionMemory(id, content string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ID != id {
		return false
	}
	if s.sessionMemory != content {
		s.sessionMemory = content
		s.version++
	}
	return true
}
