package state

// Status is a document lifecycle status.
type Status int

const (
	StatusDraft Status = iota
	StatusActive
	StatusClosed
	StatusArchived // NEW variant — must be threaded through String/Parse/transitions
)
