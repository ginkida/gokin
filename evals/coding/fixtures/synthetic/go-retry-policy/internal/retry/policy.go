package retry

import "time"

func APIBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	if attempt > 5 {
		attempt = 5
	}
	delay := time.Duration(100) * time.Millisecond
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	return delay
}

func DBBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	if attempt > 5 {
		attempt = 5
	}
	delay := time.Duration(100) * time.Millisecond
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	return delay
}
