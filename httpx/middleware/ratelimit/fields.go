package ratelimit

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// Policy is a quota the RateLimit-Policy field names
// (draft-ietf-httpapi-ratelimit-headers): Quota requests per Window.
type Policy struct {
	Name   string
	Quota  int
	Window time.Duration
}

// PerMinute is the policy "minute": quota requests in a 60-second window.
func PerMinute(quota int) Policy {
	return Policy{Name: "minute", Quota: quota, Window: time.Minute}
}

func (p Policy) valid() bool {
	return p.Name != "" && p.Quota > 0 && p.Window > 0
}

func (p Policy) name() string {
	return strconv.Quote(p.Name)
}

func seconds(d time.Duration) int64 {
	return max(1, int64(math.Ceil(d.Seconds())))
}

// SetPolicy answers the RateLimit-Policy field: "minute";q=120;w=60.
func SetPolicy(h http.Header, p Policy) {
	h.Set("RateLimit-Policy", p.name()+";q="+strconv.Itoa(p.Quota)+";w="+strconv.FormatInt(seconds(p.Window), 10))
}

// SetRemaining answers the RateLimit field: the requests left under the
// policy and, when the budget is spent, the time until it admits one again.
func SetRemaining(h http.Header, p Policy, remaining int64, reset time.Duration) {
	value := p.name() + ";r=" + strconv.FormatInt(max(0, remaining), 10)
	if reset > 0 {
		value += ";t=" + strconv.FormatInt(seconds(reset), 10)
	}
	h.Set("RateLimit", value)
}
