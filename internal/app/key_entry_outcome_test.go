package app

import (
	"errors"
	"strings"
	"testing"
)

func TestKeyEntryCommandOutcomeClassifiesTextProtocol(t *testing.T) {
	tests := []struct {
		name                  string
		result                string
		err                   error
		wantSuccess, wantWarn bool
		want                  string
	}{
		{name: "saved", result: "✓ GLM API key saved (glm-…1234)\n\nActive provider: GLM", wantSuccess: true, want: "GLM API key saved"},
		{name: "session warning", result: "⚠ GLM key is active for THIS session, but it could NOT be saved", wantSuccess: true, wantWarn: true, want: "active for THIS session"},
		{name: "validation", result: "Invalid API key format (too short).", want: "Invalid API key"},
		{name: "command error", err: errors.New("provider unavailable"), want: "Login failed: provider unavailable"},
		{name: "empty", want: "without a response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			success, warning, message := keyEntryCommandOutcome(tt.result, tt.err)
			if success != tt.wantSuccess || warning != tt.wantWarn || !strings.Contains(message, tt.want) {
				t.Fatalf("outcome success=%v warning=%v message=%q", success, warning, message)
			}
		})
	}
}
