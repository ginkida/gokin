package notify

// LogNotifier records messages in memory; used as the default sink and in
// tests for other components.
type LogNotifier struct {
	Messages []string
}

// Send appends the message to the in-memory log. It never fails.
func (l *LogNotifier) Send(msg string) error {
	l.Messages = append(l.Messages, msg)
	return nil
}
