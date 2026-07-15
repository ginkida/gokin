package ui

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestTaskTimelineProgressSanitizesInvalidValues(t *testing.T) {
	m := NewModel()

	for name, progress := range map[string]float64{
		"nan":      math.NaN(),
		"positive": math.Inf(1),
		"negative": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			got := stripAnsi(m.renderTaskTimelineProgress(progress, ""))
			if !strings.Contains(got, "Working…") {
				t.Fatalf("indeterminate progress needs an explicit state: %q", got)
			}
		})
	}

	got := stripAnsi(m.renderTaskTimelineProgress(4.2, "  Finishing\n work  "))
	if !strings.Contains(got, "100%") || strings.Contains(got, "420%") {
		t.Fatalf("overflow progress must clamp to 100%%: %q", got)
	}
	if !strings.Contains(got, "Finishing work") {
		t.Fatalf("progress message must be normalized: %q", got)
	}
}

func TestTaskTimelineFailureWithoutDetailsIsExplicit(t *testing.T) {
	m := NewModel()
	got := stripAnsi(m.renderTaskTimelineDone(false, -time.Second, errors.New("  \n ")))
	if !strings.Contains(got, "Failed · No error details") {
		t.Fatalf("failed state must not render an empty error separator: %q", got)
	}
}

func TestTaskTimelineLifecycleIgnoresLateAndDuplicateEvents(t *testing.T) {
	m := NewModel()
	m.handleTaskStarted(TaskStartedEvent{TaskID: "first", Message: "First"})
	m.handleTaskStarted(TaskStartedEvent{TaskID: "second", Message: "Second"})
	m.handleTaskStarted(TaskStartedEvent{TaskID: "second", Message: "Second restarted"})
	if len(m.coordinatedTaskOrder) != 2 {
		t.Fatalf("duplicate start must not duplicate task order: %v", m.coordinatedTaskOrder)
	}

	cmd := m.handleTaskCompleted(TaskCompletedEvent{TaskID: "second", Success: true})
	if cmd == nil {
		t.Fatal("tracked terminal event must schedule cleanup")
	}
	if m.activeCoordinatedTask != "first" {
		t.Fatalf("finishing the active task must restore the latest running task, got %q", m.activeCoordinatedTask)
	}
	if m.coordinatedTasks["second"].Duration <= 0 {
		t.Fatal("missing event duration must fall back to tracked elapsed time")
	}

	before := m.output.state.content.String()
	if duplicateCmd := m.handleTaskCompleted(TaskCompletedEvent{TaskID: "second", Success: true}); duplicateCmd != nil {
		t.Fatal("duplicate terminal event must be a no-op")
	}
	m.handleTaskProgress(TaskProgressEvent{TaskID: "second", Progress: .5, Message: "late progress"})
	after := m.output.state.content.String()
	if after != before {
		t.Fatalf("late events must not append stale timeline output:\nbefore=%q\nafter=%q", before, after)
	}
	if cmd := m.handleTaskCompleted(TaskCompletedEvent{TaskID: "missing", Success: false}); cmd != nil {
		t.Fatal("unknown terminal event must not schedule meaningless cleanup")
	}
}

func TestRenderTodosFitsEveryTerminalWidth(t *testing.T) {
	for width := 1; width <= 48; width++ {
		m := panelTestModel()
		m.width = width
		m.height = 30
		m.todoItems = []string{
			"- [/]   Проверить очень длинное активное действие с unicode界界界   ",
			"- [ ] next\nstep with hostile whitespace",
			"- [x] finished",
		}

		got := m.renderTodos()
		for lineNo, line := range strings.Split(got, "\n") {
			if lineWidth := lipgloss.Width(line); lineWidth > width {
				t.Fatalf("width=%d line=%d overflow=%d: %q", width, lineNo, lineWidth, stripAnsi(line))
			}
		}
	}
}

func TestRenderTodosShortTerminalPrioritizesActiveWork(t *testing.T) {
	m := panelTestModel()
	m.width = 42
	m.height = 12
	for i := 0; i < 10; i++ {
		m.todoItems = append(m.todoItems, "- [ ] queued task "+string(rune('a'+i)))
	}
	m.todoItems = append(m.todoItems, "- [/] important active task")

	got := stripAnsi(m.renderTodos())
	if !strings.Contains(got, "important active task") {
		t.Fatalf("compact panel must preserve active work:\n%s", got)
	}
	if !strings.Contains(got, "… 7 more") || !strings.Contains(got, "Ctrl+T hide") {
		t.Fatalf("compact panel must disclose folded rows and exit key:\n%s", got)
	}
	if lines := strings.Count(got, "\n") + 1; lines > 8 {
		t.Fatalf("short-terminal panel rendered %d rows, want <=8:\n%s", lines, got)
	}
}

func TestRenderTodosFitsEveryTinyTerminalHeight(t *testing.T) {
	for height := 1; height <= 12; height++ {
		m := panelTestModel()
		m.width = 48
		m.height = height
		for i := 0; i < 9; i++ {
			m.todoItems = append(m.todoItems, "- [ ] queued task "+string(rune('a'+i)))
		}
		m.todoItems = append(m.todoItems, "- [/] current active task")

		view := m.renderTodos()
		if got, limit := lipgloss.Height(view), todosPanelHeightBudget(height); got > limit {
			t.Fatalf("height=%d rendered %d rows, want <=%d:\n%s", height, got, limit, stripAnsi(view))
		}
		plain := stripAnsi(view)
		if !strings.Contains(plain, "Ctrl+T hide") {
			t.Fatalf("height=%d lost inverse action:\n%s", height, plain)
		}
		if height >= 4 && !strings.Contains(plain, "current active task") {
			t.Fatalf("height=%d lost active task:\n%s", height, plain)
		}
	}
}

func TestRenderTodosNarrowFooterKeepsHideBeforeCounts(t *testing.T) {
	m := panelTestModel()
	m.width = 22
	m.height = 12
	for i := 0; i < 9; i++ {
		m.todoItems = append(m.todoItems, "- [x] completed task")
	}
	m.todoItems = append(m.todoItems, "- [/] current task", "- [ ] next task")

	plain := stripAnsi(m.renderTodos())
	if !strings.Contains(plain, "Ctrl+T hide") {
		t.Fatalf("narrow footer truncated its recovery action:\n%s", plain)
	}
}
