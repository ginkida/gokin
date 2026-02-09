package commands

import (
	"fmt"
	"time"
)

// formatTime formats a unix timestamp in a human-readable way.
func formatTime(unix int64) string {
	if unix == 0 {
		return "never"
	}

	t := time.Unix(unix, 0)
	age := time.Since(t)

	if age < time.Minute {
		return fmt.Sprintf("%ds ago", int(age.Seconds()))
	} else if age < time.Hour {
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	} else if age < 24*time.Hour {
		hours := int(age.Hours())
		minutes := int(age.Minutes()) % 60
		return fmt.Sprintf("%dh %dm ago", hours, minutes)
	} else if age < 30*24*time.Hour {
		days := int(age.Hours() / 24)
		hours := int(age.Hours()) % 24
		return fmt.Sprintf("%dd %dh ago", days, hours)
	} else {
		return t.Format("2006-01-02")
	}
}
