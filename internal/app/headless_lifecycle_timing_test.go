package app

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"gokin/internal/lifecycle"
)

func TestSDKProgressAndResultIncludeLifecycleDiagnostics(t *testing.T) {
	var stdout bytes.Buffer
	presenter := newSDKJSONPresenter(&stdout, "session", false, false)
	presenter.Initialize("/workspace", "mock", "default", nil, nil)
	var event struct {
		Timestamp time.Time `json:"timestamp"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &event); err != nil || event.Timestamp.IsZero() {
		t.Fatalf("missing SDK timestamp: %s err=%v", stdout.String(), err)
	}
	result := SDKResult(HeadlessResult{Status: "error", Incomplete: true, ClaimMismatch: true, Timing: lifecycle.Timing{StartupMS: 25, RunMS: 75}}, 0, 0)
	if result["claim_mismatch"] != true || result["timing"].(lifecycle.Timing).StartupMS != 25 || result["is_error"] != true {
		t.Fatalf("missing SDK receipt: %+v", result)
	}
}
