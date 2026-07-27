package tools

import (
	"sync"
	"testing"
)

func TestNativeNotificationsPreferenceCanChangeLive(t *testing.T) {
	manager := NewNotificationManager()
	if manager.NativeNotificationsEnabled() {
		t.Fatal("native notifications should be opt-in")
	}

	manager.EnableNativeNotifications(true)
	if !manager.NativeNotificationsEnabled() {
		t.Fatal("enabling native notifications did not update the manager")
	}
	manager.EnableNativeNotifications(false)
	if manager.NativeNotificationsEnabled() {
		t.Fatal("disabling native notifications did not update the manager")
	}
}

func TestNotificationCallbackReplacementIsConcurrentSafe(t *testing.T) {
	manager := NewNotificationManager()
	var wg sync.WaitGroup

	for range 100 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			manager.SetOnNotify(func(Notification) {})
		}()
		go func() {
			defer wg.Done()
			manager.NotifyError("tool", "failed", "test error")
		}()
	}
	wg.Wait()
}
