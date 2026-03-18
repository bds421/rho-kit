package logattr

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAttrKeys(t *testing.T) {
	tests := []struct {
		name string
		attr slog.Attr
		key  string
	}{
		{"Error", Error(errors.New("boom")), "error"},
		{"Component", Component("http"), "component"},
		{"RequestID", RequestID("abc-123"), "request_id"},
		{"Addr", Addr(":8080"), "addr"},
		{"Attempt", Attempt(3), "attempt"},
		{"Delay", Delay(5 * time.Second), "delay"},
		{"Method", Method("GET"), "method"},
		{"Path", Path("/api/users"), "path"},
		{"StatusCode", StatusCode(200), "status"},
		{"Instance", Instance("primary"), "instance"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.key, tt.attr.Key)
		})
	}
}

func TestAttrValues(t *testing.T) {
	assert.Equal(t, "http", Component("http").Value.String())
	assert.Equal(t, int64(3), Attempt(3).Value.Int64())
	assert.Equal(t, int64(200), StatusCode(200).Value.Int64())
}
