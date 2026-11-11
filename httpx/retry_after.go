package httpx

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ParseRetryAfter reads a Retry-After header value: seconds, with a
// fraction some services send, rounded up, or an HTTP date, as the wait
// from now. A missing, negative or unreadable value is not a wait.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds < 0 || math.IsNaN(seconds) || seconds > math.MaxInt32 {
			return 0, false
		}
		return time.Duration(math.Ceil(seconds)) * time.Second, true
	}
	at, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(at.Sub(now), 0), true
}
