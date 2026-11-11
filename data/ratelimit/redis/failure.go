package redis

import (
	"context"
	"errors"
	"net"
	"strings"

	goredis "github.com/redis/go-redis/v9"
)

// failureClass names how a Redis call failed in a word safe to log, since the
// driver's own text can carry addresses and keys and the redacted type of a
// canceled context, a closed client and a pool timeout is the same.
func failureClass(err error) string {
	var reply goredis.Error
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, goredis.ErrPoolTimeout):
		return "pool_timeout"
	case errors.Is(err, goredis.ErrClosed):
		return "client_closed"
	case errors.As(err, &reply):
		return "reply_" + replyCode(reply.Error())
	case errors.As(err, &network) && network.Timeout():
		return "network_timeout"
	case errors.As(err, &network):
		return "network"
	default:
		return "other"
	}
}

// replyCode is the error code a Redis reply starts with (NOAUTH, OOM,
// NOSCRIPT, ERR …), never the text after it.
func replyCode(reply string) string {
	code, _, _ := strings.Cut(reply, " ")
	if code == "" || len(code) > 16 || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return "unknown"
	}
	return code
}
