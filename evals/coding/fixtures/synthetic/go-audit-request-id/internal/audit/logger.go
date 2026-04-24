package audit

type Event struct {
	Action string
	UserID string
}

type Logger struct {
	events []Event
}

func NewLogger() *Logger {
	return &Logger{}
}

func (l *Logger) Record(action, userID string) {
	l.events = append(l.events, Event{Action: action, UserID: userID})
}

func (l *Logger) Events() []Event {
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}
