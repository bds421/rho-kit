package redis

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	goredis "github.com/redis/go-redis/v9"
)

type replyError string

func (e replyError) Error() string { return string(e) }
func (replyError) RedisError()     {}

func TestFailureClassNamesTheCauseWithoutItsText(t *testing.T) {
	for want, err := range map[string]error{
		"context_canceled":  fmt.Errorf("script: %w", context.Canceled),
		"deadline_exceeded": context.DeadlineExceeded,
		"pool_timeout":      goredis.ErrPoolTimeout,
		"client_closed":     goredis.ErrClosed,
		"reply_NOAUTH":      replyError("NOAUTH Authentication required."),
		"reply_unknown":     replyError("weird reply with secret"),
		"network":           &net.OpError{Op: "dial", Err: errors.New("refused")},
		"other":             errors.New("something"),
	} {
		if got := failureClass(err); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
}
