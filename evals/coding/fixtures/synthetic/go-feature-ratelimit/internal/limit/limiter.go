package limit

// Limiter is a token-bucket rate limiter. NewLimiter(capacity) starts with
// a full bucket; Allow consumes one token and reports whether one was
// available; Refill adds tokens up to capacity.
type Limiter struct {
	capacity int
	tokens   int
}

// NewLimiter returns a limiter with a full bucket of the given capacity.
func NewLimiter(capacity int) *Limiter {
	return &Limiter{capacity: capacity, tokens: capacity}
}

// Allow consumes one token. TODO: implement token accounting.
func (l *Limiter) Allow() bool {
	return true
}

// Refill adds n tokens, never exceeding capacity. TODO: implement.
func (l *Limiter) Refill(n int) {
}
