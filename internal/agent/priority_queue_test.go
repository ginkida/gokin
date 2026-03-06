package agent

import (
	"testing"
)

func TestNewTaskQueue(t *testing.T) {
	q := NewTaskQueue()
	if q.Size() != 0 {
		t.Errorf("new queue size = %d", q.Size())
	}
}

func TestTaskQueuePushPop(t *testing.T) {
	q := NewTaskQueue()

	q.PushTask(&CoordinatedTask{ID: "low", Priority: PriorityLow})
	q.PushTask(&CoordinatedTask{ID: "high", Priority: PriorityHigh})
	q.PushTask(&CoordinatedTask{ID: "normal", Priority: PriorityNormal})

	if q.Size() != 3 {
		t.Fatalf("size = %d, want 3", q.Size())
	}

	// Pop should return highest priority first
	task := q.PopTask()
	if task.ID != "high" {
		t.Errorf("first pop = %q, want high", task.ID)
	}

	task = q.PopTask()
	if task.ID != "normal" {
		t.Errorf("second pop = %q, want normal", task.ID)
	}

	task = q.PopTask()
	if task.ID != "low" {
		t.Errorf("third pop = %q, want low", task.ID)
	}

	if q.Size() != 0 {
		t.Error("queue should be empty")
	}
}

func TestTaskQueuePopEmpty(t *testing.T) {
	q := NewTaskQueue()
	task := q.PopTask()
	if task != nil {
		t.Error("pop from empty queue should return nil")
	}
}

func TestTaskQueuePeekTask(t *testing.T) {
	q := NewTaskQueue()

	if q.PeekTask() != nil {
		t.Error("peek empty should return nil")
	}

	q.PushTask(&CoordinatedTask{ID: "low", Priority: PriorityLow})
	q.PushTask(&CoordinatedTask{ID: "high", Priority: PriorityHigh})

	peeked := q.PeekTask()
	if peeked.ID != "high" {
		t.Errorf("peek = %q, want high", peeked.ID)
	}

	// Peek should not remove
	if q.Size() != 2 {
		t.Error("peek should not modify queue")
	}
}

func TestTaskQueueUpdatePriority(t *testing.T) {
	q := NewTaskQueue()

	low := &CoordinatedTask{ID: "low", Priority: PriorityLow}
	high := &CoordinatedTask{ID: "high", Priority: PriorityHigh}
	q.PushTask(low)
	q.PushTask(high)

	// Boost low to higher than high
	ok := q.UpdatePriority(low, 15)
	if !ok {
		t.Fatal("UpdatePriority should succeed")
	}

	// Now low should come first
	task := q.PopTask()
	if task.ID != "low" {
		t.Errorf("after boost, first = %q, want low", task.ID)
	}
}

func TestTaskQueueUpdatePriorityInvalid(t *testing.T) {
	q := NewTaskQueue()

	task := &CoordinatedTask{ID: "orphan", Priority: PriorityNormal, index: -1}
	ok := q.UpdatePriority(task, PriorityHigh)
	if ok {
		t.Error("should fail for task not in queue")
	}
}

func TestTaskQueueRemoveTask(t *testing.T) {
	q := NewTaskQueue()
	q.PushTask(&CoordinatedTask{ID: "a", Priority: PriorityNormal})
	q.PushTask(&CoordinatedTask{ID: "b", Priority: PriorityHigh})
	q.PushTask(&CoordinatedTask{ID: "c", Priority: PriorityLow})

	removed := q.RemoveTask("b")
	if removed == nil || removed.ID != "b" {
		t.Error("should remove task b")
	}
	if q.Size() != 2 {
		t.Errorf("size after remove = %d", q.Size())
	}

	// Remove nonexistent
	removed = q.RemoveTask("x")
	if removed != nil {
		t.Error("removing nonexistent should return nil")
	}
}

func TestTaskQueueGetReadyTasks(t *testing.T) {
	q := NewTaskQueue()
	q.PushTask(&CoordinatedTask{ID: "a", Priority: PriorityHigh, Status: TaskStatusReady})
	q.PushTask(&CoordinatedTask{ID: "b", Priority: PriorityNormal, Status: TaskStatusPending})
	q.PushTask(&CoordinatedTask{ID: "c", Priority: PriorityLow, Status: TaskStatusReady})

	ready := q.GetReadyTasks()
	if len(ready) != 2 {
		t.Errorf("ready = %d, want 2", len(ready))
	}
}

func TestTaskQueueSamePriority(t *testing.T) {
	q := NewTaskQueue()
	q.PushTask(&CoordinatedTask{ID: "a", Priority: PriorityNormal})
	q.PushTask(&CoordinatedTask{ID: "b", Priority: PriorityNormal})

	if q.Size() != 2 {
		t.Errorf("size = %d", q.Size())
	}

	// Both should be poppable
	q.PopTask()
	q.PopTask()
	if q.Size() != 0 {
		t.Error("should be empty after popping all")
	}
}
