package app

import (
	"bytes"
	"testing"

	"gokin/internal/config"
	"gokin/internal/logging"
	"gokin/internal/testkit"
)

func TestPrepareHeadlessRuntimePreservesCLIDebugLogging(t *testing.T) {
	var output bytes.Buffer
	logging.Configure(logging.LevelDebug, &output)
	t.Cleanup(logging.DisableLogging)

	mock := testkit.NewMockClient()
	application, _ := newHeadlessPolicyTestApp(
		t, mock, &appHeadlessScriptedTool{name: "unused"})
	if application.config == nil {
		application.config = config.DefaultConfig()
	}
	application.config.Debug = true

	application.prepareHeadlessRuntime()
	logging.Debug("headless debug survives", "category", "test")

	if !bytes.Contains(output.Bytes(), []byte("headless debug survives")) {
		t.Fatalf("headless setup disabled CLI debug logger:\n%s", output.String())
	}
}
