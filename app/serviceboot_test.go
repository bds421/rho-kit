package app

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResolveHealthCheckPort verifies that the --health probe targets the
// configurable INTERNAL_PORT (default 9090), mirroring LoadBaseConfig, so a
// service that overrides INTERNAL_PORT and uses the documented Docker
// HEALTHCHECK --health flag probes the right port.
func TestResolveHealthCheckPort(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     int
	}{
		{
			name:     "empty defaults to 9090",
			envValue: "",
			want:     defaultInternalPort,
		},
		{
			name:     "honors overridden INTERNAL_PORT",
			envValue: "9191",
			want:     9191,
		},
		{
			name:     "invalid value falls back to default",
			envValue: "not-a-number",
			want:     defaultInternalPort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("INTERNAL_PORT", tt.envValue)
			got := resolveHealthCheckPort()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLogApplicationErrorShowsSafeMessagesOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	b := New("svc", "v1", BaseConfig{})

	err := fmt.Errorf("start: %w", errors.Join(
		b.Validate(),
		fmt.Errorf("dial: %w", errors.New("nats://user:hunter2@broker")),
	))
	logApplicationError(logger, err)

	out := buf.String()
	assert.Contains(t, out, `error_message="invalid server port"`)
	assert.NotContains(t, out, "hunter2")
	assert.NotContains(t, out, "start:")
}
