package state

// String renders a Status as its lowercase name.
func (s Status) String() string {
	switch s {
	case StatusDraft:
		return "draft"
	case StatusActive:
		return "active"
	case StatusClosed:
		return "closed"
	default:
		return "unknown"
	}
}
