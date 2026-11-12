package pgx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransient(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, true},
		{"wrapped deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), true},
		{"connect error", &pgconn.ConnectError{}, true},
		{"network error", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, true},
		{"connection failure", &pgconn.PgError{Code: "08006"}, true},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"too many connections", &pgconn.PgError{Code: "53300"}, true},
		{"io error", &pgconn.PgError{Code: "58030"}, true},
		{"invalid byte sequence", fmt.Errorf("index: %w", &pgconn.PgError{Code: "22021"}), false},
		{"value too long", &pgconn.PgError{Code: "22001"}, false},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"permission denied", &pgconn.PgError{Code: "42501"}, false},
		{"malformed code", &pgconn.PgError{Code: "4"}, false},
		{"other error", errors.New("an observation without keys"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := IsTransient(c.err); got != c.want {
				t.Errorf("IsTransient(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
