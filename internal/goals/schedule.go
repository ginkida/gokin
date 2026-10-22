package goals

import (
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	RetryLimit             = 3
	IdleCheckInLimit       = 3
	DefaultCheckInInterval = 30 * time.Minute
)

func ParseCheckInInterval(raw string) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return DefaultCheckInInterval
	}
	minutes, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes < 0 || minutes > float64(math.MaxInt64/4)/float64(time.Minute) {
		return DefaultCheckInInterval
	}
	if minutes == 0 {
		return 0
	}
	return max(time.Nanosecond, time.Duration(minutes*float64(time.Minute)))
}

func RetryDelay(attempt int) time.Duration {
	return 15 * time.Second << min(max(attempt-1, 0), RetryLimit-1)
}

func DeferBackground(goal *Goal, now time.Time, interval time.Duration) {
	if goal.WaitingSince.IsZero() {
		goal.WaitingSince = now
	}
	if interval == 0 {
		goal.NextCheckIn = time.Time{}
		return
	}
	if goal.NextCheckIn.IsZero() {
		goal.NextCheckIn = now.Add(interval << goal.CheckInBackoff)
	}
}

func CheckInDue(goal *Goal, now time.Time) bool {
	return !goal.WaitingSince.IsZero() && !goal.NextCheckIn.IsZero() && !now.Before(goal.NextCheckIn)
}

func RecordCheckIn(goal *Goal, now time.Time, interval time.Duration, idle bool) bool {
	if interval <= 0 || !CheckInDue(goal, now) || idle && goal.IdleCheckIns >= IdleCheckInLimit {
		return false
	}
	if idle {
		goal.IdleCheckIns++
	}
	goal.CheckInBackoff = min(goal.CheckInBackoff+1, 2)
	goal.NextCheckIn = now.Add(interval << goal.CheckInBackoff)
	return true
}
