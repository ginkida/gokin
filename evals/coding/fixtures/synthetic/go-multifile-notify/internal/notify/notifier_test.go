package notify

import "testing"

func TestLogNotifierRecords(t *testing.T) {
	l := &LogNotifier{}
	if err := l.Send("hello"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(l.Messages) != 1 || l.Messages[0] != "hello" {
		t.Fatalf("Messages = %v, want [hello]", l.Messages)
	}
}

// EmailNotifier is specified here ahead of its implementation: it must be
// constructed with NewEmailNotifier(addr, sender), validate that the address
// is non-empty on Send (returning an error mentioning "address"), and
// deliver through the injected sender func.
func TestEmailNotifierSendsThroughSender(t *testing.T) {
	var delivered []string
	sender := func(addr, msg string) error {
		delivered = append(delivered, addr+": "+msg)
		return nil
	}

	n := NewEmailNotifier("ops@example.com", sender)
	if err := n.Send("disk full"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(delivered) != 1 || delivered[0] != "ops@example.com: disk full" {
		t.Fatalf("delivered = %v", delivered)
	}
}

func TestEmailNotifierRejectsEmptyAddress(t *testing.T) {
	n := NewEmailNotifier("", func(addr, msg string) error { return nil })
	err := n.Send("anything")
	if err == nil {
		t.Fatal("Send() with empty address must fail")
	}
}

// Both notifiers must satisfy the shared interface.
var (
	_ Notifier = (*LogNotifier)(nil)
	_ Notifier = (*EmailNotifier)(nil)
)
