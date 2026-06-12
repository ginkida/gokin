package model

// Event is the wire format for audit events. The JSON field names are a
// public contract consumed by downstream services — they must not change.
type Event struct {
	ID        string `json:"id"`
	EventType string `json:"event_tyep"`
	Payload   string `json:"payload,omitempty"`
}
