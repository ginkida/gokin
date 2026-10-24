package lifecycle

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTimingWarnsOncePerSlowStageAndSeparatesDurations(t *testing.T) {
	var warnings bytes.Buffer
	trace := newTrace(&warnings, 20*time.Millisecond)
	trace.Phase("mcp_connect")
	time.Sleep(35 * time.Millisecond)
	trace.StartTurn()
	trace.Phase("session_save")
	time.Sleep(5 * time.Millisecond)
	resultTiming := trace.Snapshot()
	if resultTiming.StartupMS < 20 || resultTiming.RunMS < 1 || resultTiming.ShutdownMS != nil {
		t.Fatalf("invalid pre-shutdown receipt: %+v", resultTiming)
	}
	trace.EndTurn()
	trace.Phase("session_end_hooks")
	time.Sleep(35 * time.Millisecond)
	finished := trace.Finish()
	trace.Finish()
	if finished.ShutdownMS == nil || *finished.ShutdownMS < 20 || finished.PhasesMS["run.session_save"] < 1 {
		t.Fatalf("lost shutdown/persistence time: %+v", finished)
	}
	if text := warnings.String(); strings.Count(text, "Warning:") != 2 || !strings.Contains(text, "slowest phase=mcp_connect") || !strings.Contains(text, "slowest phase=session_end_hooks") {
		t.Fatalf("warnings=%q", text)
	}
	encoded, _ := json.Marshal(resultTiming)
	if strings.Contains(string(encoded), "shutdown_ms") {
		t.Fatal("result invented future shutdown duration")
	}
}

func TestTimingCountsMultipleTurnsWithoutCallingInterturnTimeShutdown(t *testing.T) {
	trace := New(nil)
	trace.StartTurn()
	time.Sleep(time.Millisecond)
	trace.EndTurn()
	time.Sleep(3 * time.Millisecond)
	trace.StartTurn()
	time.Sleep(time.Millisecond)
	trace.EndTurn()
	result := trace.Finish()
	if result.RunMS < 2 || result.BetweenTurnsMS < 3 || result.ShutdownMS == nil {
		t.Fatalf("invalid multi-turn timing: %+v", result)
	}
}

func TestTimingIncludesKnownDetachedLaunchGap(t *testing.T) {
	var warnings bytes.Buffer
	trace := New(&warnings)
	trace.IncludeLaunch(time.Now().Add(-20 * time.Second))
	trace.StartTurn()
	trace.EndTurn()
	finished := trace.Finish()
	if finished.StartupMS < 20000 || finished.PhasesMS["startup.worker_launch"] < 19000 || !strings.Contains(warnings.String(), "slowest phase=worker_launch") || strings.Count(warnings.String(), "Warning:") != 1 {
		t.Fatalf("launch gap was hidden: timing=%+v warnings=%s", finished, warnings.String())
	}
}
