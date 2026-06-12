package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEventMarshalUsesContractFieldNames(t *testing.T) {
	in := Event{ID: "evt-1", EventType: "user.created", Payload: "{}"}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal(%+v) error: %v", in, err)
	}
	if !strings.Contains(string(data), `"event_type"`) {
		t.Fatalf("marshaled JSON %s does not contain the contract field %q", data, "event_type")
	}
}

func TestEventRoundTrip(t *testing.T) {
	wire := `{"id":"evt-2","event_type":"user.deleted"}`

	var out Event
	if err := json.Unmarshal([]byte(wire), &out); err != nil {
		t.Fatalf("Unmarshal(%s) error: %v", wire, err)
	}
	if out.EventType != "user.deleted" {
		t.Fatalf("Unmarshal(%s).EventType = %q, want %q", wire, out.EventType, "user.deleted")
	}
}
