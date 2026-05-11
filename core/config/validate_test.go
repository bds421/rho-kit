package config

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateURLHost(t *testing.T) {
	t.Parallel()

	valid, err := url.Parse("rediss://redis.example.com:6380/0")
	require.NoError(t, err)
	validNoPort, err := url.Parse("rediss://redis.example.com/0")
	require.NoError(t, err)
	validIPv6, err := url.Parse("rediss://[2001:db8::1]:6380/0")
	require.NoError(t, err)

	tests := []struct {
		name    string
		u       *url.URL
		wantErr bool
	}{
		{name: "valid host and port", u: valid},
		{name: "valid host without port", u: validNoPort},
		{name: "valid bracketed ipv6", u: validIPv6},
		{name: "nil URL", wantErr: true},
		{name: "empty host", u: &url.URL{Scheme: "rediss"}, wantErr: true},
		{name: "empty hostname", u: &url.URL{Scheme: "rediss", Host: ":6380"}, wantErr: true},
		{name: "empty port", u: &url.URL{Scheme: "rediss", Host: "redis.example.com:"}, wantErr: true},
		{name: "unbracketed ipv6", u: &url.URL{Scheme: "rediss", Host: "::1"}, wantErr: true},
		{name: "zero port", u: &url.URL{Scheme: "rediss", Host: "redis.example.com:0"}, wantErr: true},
		{name: "too large port", u: &url.URL{Scheme: "rediss", Host: "redis.example.com:65536"}, wantErr: true},
		{name: "percent host", u: &url.URL{Scheme: "rediss", Host: "fe80::1%25lo0"}, wantErr: true},
		{name: "control host", u: &url.URL{Scheme: "rediss", Host: "redis.example.com\nx"}, wantErr: true},
		{name: "invalid utf8 host", u: &url.URL{Scheme: "rediss", Host: string([]byte{'r', 'e', 'd', 'i', 's', 0xff})}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateURLHost("TEST_URL", tt.u)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateNumericErrorsDoNotReflectValues(t *testing.T) {
	t.Parallel()

	err := ValidatePort("REDIS", 65536)
	require.Error(t, err)
	assert.EqualError(t, err, "invalid REDIS port")
	assert.NotContains(t, err.Error(), "65536")

	err = ValidatePositive("WORKERS", -10)
	require.Error(t, err)
	assert.EqualError(t, err, "WORKERS must be positive")
	assert.NotContains(t, err.Error(), "-10")

	err = ValidateURLHost("SERVICE_URL", &url.URL{Scheme: "https", Host: "example.com:99999"})
	require.Error(t, err)
	assert.EqualError(t, err, "SERVICE_URL port is invalid")
	assert.NotContains(t, err.Error(), "99999")
}
