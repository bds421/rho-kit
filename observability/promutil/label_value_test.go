package promutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateStaticLabelValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
		ok    bool
	}{
		{name: "simple", value: "jobs.reconcile", ok: true},
		{name: "max length", value: strings.Repeat("a", MaxStaticLabelValueBytes), ok: true},
		{name: "empty", value: ""},
		{name: "too long", value: strings.Repeat("a", MaxStaticLabelValueBytes+1)},
		{name: "invalid utf8", value: string([]byte{0xff})},
		{name: "nul", value: "job\x00name"},
		{name: "newline", value: "job\nname"},
		{name: "carriage return", value: "job\rname"},
		{name: "space", value: "job name"},
		{name: "tab", value: "job\tname"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateStaticLabelValue("job name", tt.value)
			if tt.ok {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, ErrInvalidLabelValue)
			if tt.name == "too long" {
				assert.NotContains(t, err.Error(), "256")
				assert.NotContains(t, err.Error(), "257")
			}
		})
	}
}

func TestValidateMetricNamePart(t *testing.T) {
	tests := []struct {
		name  string
		value string
		ok    bool
	}{
		{name: "empty optional", value: "", ok: true},
		{name: "simple", value: "http", ok: true},
		{name: "underscore", value: "file_copier", ok: true},
		{name: "leading underscore", value: "_internal", ok: true},
		{name: "digit suffix", value: "worker2", ok: true},
		{name: "starts with digit", value: "2worker"},
		{name: "hyphen", value: "my-service"},
		{name: "space", value: "my service"},
		{name: "colon", value: "go:runtime"},
		{name: "invalid utf8", value: string([]byte{0xff})},
		{name: "newline", value: "job\nname"},
		{name: "secret", value: "secret-token-name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMetricNamePart("namespace", tt.value)
			if tt.ok {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, ErrInvalidMetricNamePart)
			assert.NotContains(t, err.Error(), tt.value)
			assert.NotContains(t, err.Error(), "secret-token")
		})
	}
}

func TestHTTPMethodLabel(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{method: "GET", want: "GET"},
		{method: "POST", want: "POST"},
		{method: "PATCH", want: "PATCH"},
		{method: "BREW", want: OtherHTTPMethodLabel},
		{method: "GET\nX", want: OtherHTTPMethodLabel},
		{method: strings.Repeat("A", 1024), want: OtherHTTPMethodLabel},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			assert.Equal(t, tt.want, HTTPMethodLabel(tt.method))
		})
	}
}

func TestOpaqueLabelValue_HashesOpaqueParts(t *testing.T) {
	got := OpaqueLabelValue("queue", "email:priority.high", "tenant-secret")
	assert.NoError(t, ValidateStaticLabelValue("queue", got))
	assert.Regexp(t, `^queue-[0-9a-f]{12}$`, got)
	for _, reflected := range []string{"email", "priority", "high", "tenant", "secret"} {
		assert.NotContains(t, got, reflected)
	}
	assert.Equal(t, got, OpaqueLabelValue("queue", "email:priority.high", "tenant-secret"))
}

func TestOpaqueLabelValue_NormalizesStaticPrefix(t *testing.T) {
	assert.Equal(t, "queue-depth", OpaqueLabelValue("Queue Depth"))
	assert.Equal(t, "value", OpaqueLabelValue(""))
}

func TestOpaqueLabelValue_TruncatesLongPrefix(t *testing.T) {
	got := OpaqueLabelValue(strings.Repeat("segment-", 80), "secret-token")
	assert.NoError(t, ValidateStaticLabelValue("label", got))
	assert.LessOrEqual(t, len(got), MaxStaticLabelValueBytes)
	assert.NotContains(t, got, "secret-token")
}
