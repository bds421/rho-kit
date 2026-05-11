package ratelimit_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/data/v2/ratelimit"
)

func TestSentinels_Distinct(t *testing.T) {
	assert.NotErrorIs(t, ratelimit.ErrInvalidLimiter, ratelimit.ErrInvalidKey)
	assert.NotErrorIs(t, ratelimit.ErrInvalidKey, ratelimit.ErrInvalidLimiter)
}

func TestValidateKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want error
	}{
		{name: "valid", key: "tenant:route:user"},
		{name: "valid max", key: strings.Repeat("a", ratelimit.MaxKeyLen)},
		{name: "empty", key: "", want: ratelimit.ErrInvalidKey},
		{name: "too long", key: strings.Repeat("a", ratelimit.MaxKeyLen+1), want: ratelimit.ErrInvalidKey},
		{name: "newline", key: "tenant\nroute", want: ratelimit.ErrInvalidKey},
		{name: "carriage", key: "tenant\rroute", want: ratelimit.ErrInvalidKey},
		{name: "null", key: "tenant\x00route", want: ratelimit.ErrInvalidKey},
		{name: "space", key: "tenant route", want: ratelimit.ErrInvalidKey},
		{name: "tab", key: "tenant\troute", want: ratelimit.ErrInvalidKey},
		{name: "invalid utf8", key: string([]byte{'t', 0xff}), want: ratelimit.ErrInvalidKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ratelimit.ValidateKey(tc.key)
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
			if tc.name == "too long" {
				assert.NotContains(t, err.Error(), "256")
				assert.NotContains(t, err.Error(), "257")
			}
		})
	}
}
