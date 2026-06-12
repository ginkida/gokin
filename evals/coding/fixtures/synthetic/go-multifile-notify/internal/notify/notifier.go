package notify

// Notifier delivers a human-readable message to some destination.
type Notifier interface {
	Send(msg string) error
}
