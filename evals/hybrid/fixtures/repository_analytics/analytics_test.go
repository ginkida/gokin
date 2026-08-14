package analytics

import "testing"

func TestFormat(t *testing.T) {
	if got := Format("ok"); got != "[ok]" {
		t.Fatalf("Format() = %q", got)
	}
}
