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

func TestSecret_RedactsValue(t *testing.T) {
	const value = "Bearer eyJhbGc.foo.bar"
	attr := Secret("authorization", value)
	assert.Equal(t, "authorization", attr.Key)
	got := attr.Value.String()
	assert.NotContains(t, got, "Bearer")
	assert.NotContains(t, got, "eyJhbGc")
	assert.Contains(t, got, "redacted")
	assert.Contains(t, got, "22 bytes")
	assert.Contains(t, got, "sha256:")
}

func TestSecret_StableHashAcrossCalls(t *testing.T) {
	a := Secret("k", "secret-value-1234").Value.String()
	b := Secret("k", "secret-value-1234").Value.String()
	assert.Equal(t, a, b, "same value must hash to the same digest")

	c := Secret("k", "secret-value-1235").Value.String()
	assert.NotEqual(t, a, c, "different values must differ")
}

func TestSecret_Empty(t *testing.T) {
	assert.Equal(t, "<redacted empty>", Secret("k", "").Value.String())
}

func TestEmail_Masking(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"alice@example.com", "a***@example.com"},
		{"a@example.com", "*@example.com"},
		{"", "<redacted empty>"},
		{"no-at-sign", "<redacted>"},
		{"@only-domain", "<redacted>"},
		{"user@", "<redacted>"},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, Email(c.in).Value.String(), "Email(%q)", c.in)
	}
}
