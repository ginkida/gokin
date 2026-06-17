package router

import (
	"testing"

	"gokin/internal/testkit"
)

// TestRouter_SetClientSwapsLiveClient pins the stale-client fix: after the
// provider/key/model changes (ApplyConfig calls SetClient), the router must
// apply per-request config (thinking budget, tools) to the NEW client, not the
// discarded pre-swap one — otherwise a /login key change silently did nothing
// in routed requests until restart.
func TestRouter_SetClientSwapsLiveClient(t *testing.T) {
	r := &Router{}

	c1 := testkit.NewMockClient()
	r.SetClient(c1)
	if r.getClient() != c1 {
		t.Fatal("getClient must return the client set by SetClient")
	}

	c2 := testkit.NewMockClient()
	r.SetClient(c2)
	if r.getClient() != c2 {
		t.Fatal("after a second SetClient, getClient must return the newest client")
	}
}
