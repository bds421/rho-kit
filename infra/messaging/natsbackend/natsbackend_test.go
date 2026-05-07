package natsbackend

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComposeSubject_RoutingKeyOptional(t *testing.T) {
	assert.Equal(t, "events", composeSubject("events", ""))
	assert.Equal(t, "events.user.created", composeSubject("events", "user.created"))
}

func TestSplitSubject_RoundTripsCompose(t *testing.T) {
	tests := []struct {
		subject  string
		exchange string
		routing  string
	}{
		{"events", "events", ""},
		{"events.user.created", "events", "user.created"},
		{"plain", "plain", ""},
	}
	for _, tt := range tests {
		ex, rk := splitSubject(tt.subject)
		assert.Equal(t, tt.exchange, ex, "subject=%q", tt.subject)
		assert.Equal(t, tt.routing, rk, "subject=%q", tt.subject)
	}
}

func TestConnect_RejectsEmptyURL(t *testing.T) {
	_, err := Connect(t.Context(), Config{})
	assert.Error(t, err)
}
