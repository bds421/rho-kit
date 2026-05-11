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

func TestErrorRedactsValue(t *testing.T) {
	attr := Error(errors.New("backend token=tenant-secret"))

	assert.Equal(t, "error", attr.Key)
	got := attr.Value.String()
	assert.Contains(t, got, "<redacted error")
	assert.NotContains(t, got, "tenant-secret")
}

func TestPathRedactsValue(t *testing.T) {
	attr := Path("/reset/secret-token")

	assert.Equal(t, "path", attr.Key)
	got := attr.Value.String()
	assert.Contains(t, got, "<redacted")
	assert.NotContains(t, got, "secret-token")
}

func TestRuntimeIdentifierAttrsRedactValues(t *testing.T) {
	tests := []slog.Attr{
		Addr("10.0.0.5:5432"),
		Instance("tenant-primary-db"),
		UserID("user-secret-123"),
		Operation("tenant.delete-secret"),
		Queue("tenant-secret-queue"),
		Topic("tenant-secret-topic"),
		Stream("tenant-secret-stream"),
	}

	for _, attr := range tests {
		got := attr.Value.String()
		assert.Contains(t, got, "<redacted", attr.Key)
		assert.NotContains(t, got, "secret", attr.Key)
		assert.NotContains(t, got, "10.0.0.5", attr.Key)
	}
}

func TestSecret_RedactsValue(t *testing.T) {
	// FR-085 [LOW]: Secret no longer carries a SHA-256 prefix —
	// brute-forceable for low-entropy values. Tests update to the
	// length-only redaction; correlation-friendly behaviour moved
	// to SecretWithDigest.
	const value = "Bearer eyJhbGc.foo.bar"
	attr := Secret("authorization", value)
	assert.Equal(t, "authorization", attr.Key)
	got := attr.Value.String()
	assert.NotContains(t, got, "Bearer")
	assert.NotContains(t, got, "eyJhbGc")
	assert.Contains(t, got, "redacted")
	assert.Contains(t, got, "22 bytes")
	assert.NotContains(t, got, "sha256:")
}

func TestSecretWithDigest_StableHashAcrossCalls(t *testing.T) {
	a := SecretWithDigest("k", "secret-value-1234").Value.String()
	b := SecretWithDigest("k", "secret-value-1234").Value.String()
	assert.Equal(t, a, b, "same value must hash to the same digest")

	c := SecretWithDigest("k", "secret-value-1235").Value.String()
	assert.NotEqual(t, a, c, "different values must differ")
}

func TestSecret_Empty(t *testing.T) {
	assert.Equal(t, "<redacted empty>", Secret("k", "").Value.String())
}

func TestURL_RedactsSensitiveComponents(t *testing.T) {
	got := URL("https://token-user:secret@example.com/api?token=query-secret#frag").Value.String()

	assert.Contains(t, got, "<redacted")
	assert.NotContains(t, got, "example.com")
	assert.NotContains(t, got, "/api")
	assert.NotContains(t, got, "token-user")
	assert.NotContains(t, got, "secret")
	assert.NotContains(t, got, "query-secret")
	assert.NotContains(t, got, "frag")
}

func TestURL_RelativePathDropsQueryAndFragment(t *testing.T) {
	got := URL("/api/users?token=secret#frag").Value.String()

	assert.Contains(t, got, "<redacted")
	assert.NotContains(t, got, "/api/users")
	assert.NotContains(t, got, "token=secret")
	assert.NotContains(t, got, "frag")
}

func TestURL_EmptyAndInvalid(t *testing.T) {
	assert.Equal(t, "<redacted empty>", URL("").Value.String())
	assert.Equal(t, "[INVALID URL]", URL("://invalid").Value.String())
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
