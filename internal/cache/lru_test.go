package cache

import (
	"testing"
	"time"
)

func TestLRUCache_SetGet(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)

	c.Set("a", 1)
	c.Set("b", 2)

	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("Get(a) = %d, %v; want 1, true", v, ok)
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("Get(b) = %d, %v; want 2, true", v, ok)
	}
	if _, ok := c.Get("c"); ok {
		t.Error("Get(c) should return false")
	}
}

func TestLRUCache_Update(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)
	c.Set("a", 1)
	c.Set("a", 2)

	v, ok := c.Get("a")
	if !ok || v != 2 {
		t.Errorf("Get(a) after update = %d, %v; want 2, true", v, ok)
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1 (update shouldn't add)", c.Len())
	}
}

func TestLRUCache_Eviction(t *testing.T) {
	c := NewLRUCache[string, int](3, time.Hour)

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	// At capacity. Adding "d" should evict "a" (oldest)
	c.Set("d", 4)

	if _, ok := c.Get("a"); ok {
		t.Error("a should be evicted")
	}
	if c.Len() != 3 {
		t.Errorf("Len = %d, want 3", c.Len())
	}
	if v, ok := c.Get("d"); !ok || v != 4 {
		t.Errorf("Get(d) = %d, %v", v, ok)
	}
}

func TestLRUCache_LRUOrder(t *testing.T) {
	c := NewLRUCache[string, int](3, time.Hour)

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	// Access "a" to make it most recently used
	c.Get("a")

	// Adding "d" should evict "b" (least recently used), not "a"
	c.Set("d", 4)

	if _, ok := c.Get("a"); !ok {
		t.Error("a should still exist (was accessed)")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("b should be evicted (least recently used)")
	}
}

func TestLRUCache_TTLExpiry(t *testing.T) {
	c := NewLRUCache[string, int](10, 50*time.Millisecond)

	c.Set("a", 1)
	// Immediately should be available
	if _, ok := c.Get("a"); !ok {
		t.Error("a should exist immediately")
	}

	// Wait for expiry
	time.Sleep(100 * time.Millisecond)

	if _, ok := c.Get("a"); ok {
		t.Error("a should be expired")
	}
}

func TestLRUCache_Delete(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)
	c.Set("a", 1)
	c.Set("b", 2)

	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Error("a should be deleted")
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}

	// Delete non-existent key should not panic
	c.Delete("nonexistent")
}

func TestLRUCache_Clear(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	c.Clear()
	if c.Len() != 0 {
		t.Errorf("after Clear, Len = %d", c.Len())
	}
	if _, ok := c.Get("a"); ok {
		t.Error("a should not exist after Clear")
	}
}

func TestLRUCache_Remove(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	// Remove entries with value > 1
	c.Remove(func(k string, v int) bool {
		return v > 1
	})

	if c.Len() != 1 {
		t.Errorf("after Remove, Len = %d, want 1", c.Len())
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a should still exist")
	}
}

func TestLRUCache_Keys(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)
	c.Set("a", 1)
	c.Set("b", 2)

	keys := c.Keys()
	if len(keys) != 2 {
		t.Errorf("Keys len = %d, want 2", len(keys))
	}
}

func TestLRUCache_KeysExcludesExpired(t *testing.T) {
	c := NewLRUCache[string, int](10, 50*time.Millisecond)
	c.Set("a", 1)
	time.Sleep(100 * time.Millisecond)
	c.Set("b", 2) // only b is not expired

	keys := c.Keys()
	if len(keys) != 1 {
		t.Errorf("Keys len = %d, want 1 (expired filtered)", len(keys))
	}
}

func TestLRUCache_Cleanup(t *testing.T) {
	c := NewLRUCache[string, int](10, 50*time.Millisecond)
	c.Set("a", 1)
	c.Set("b", 2)
	time.Sleep(100 * time.Millisecond)

	removed := c.Cleanup()
	if removed != 2 {
		t.Errorf("Cleanup removed %d, want 2", removed)
	}
	if c.Len() != 0 {
		t.Errorf("after Cleanup, Len = %d", c.Len())
	}
}

func TestLRUCache_Put(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)
	c.Put("x", 42) // Put is alias for Set
	v, ok := c.Get("x")
	if !ok || v != 42 {
		t.Errorf("Put/Get = %d, %v", v, ok)
	}
}

func TestLRUCache_Size(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)
	c.Set("a", 1)
	if c.Size() != 1 {
		t.Errorf("Size = %d, want 1", c.Size())
	}
}

func TestLRUCache_MinCapacity(t *testing.T) {
	c := NewLRUCache[string, int](0, time.Hour)
	c.Set("a", 1)
	c.Set("b", 2)
	// Capacity < 1 is set to 1, so only 1 entry should remain
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1 (min capacity)", c.Len())
	}
}
