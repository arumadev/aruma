package ratelimit

// NewHourlyLimiter allows burst requests at once, refilled at perHour: for limits slower than
// NewLimiter's one per minute, such as emails to one address.
func NewHourlyLimiter(perHour, burst int) *Limiter {
	l := NewLimiter(0, burst)
	l.rate = float64(perHour) / 3600
	return l
}
