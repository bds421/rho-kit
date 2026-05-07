package secret

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	plain         = "super-secret-token"
	redactedValue = "<redacted>"
)

func TestNew_CopiesInputBytes(t *testing.T) {
	src := []byte(plain)
	s := New(src)
	src[0] = 'X' // mutate caller's buffer

	assert.Equal(t, plain, s.RevealString(), "internal buffer must not share storage with caller")
}

func TestNewFromString_StoresValue(t *testing.T) {
	s := NewFromString(plain)
	assert.Equal(t, plain, s.RevealString())
}

func TestEmptyConstructors(t *testing.T) {
	assert.True(t, New(nil).IsEmpty())
	assert.True(t, New([]byte{}).IsEmpty())
	assert.True(t, NewFromString("").IsEmpty())
	assert.Empty(t, New(nil).Reveal())
	assert.Equal(t, "", New(nil).RevealString())
}

func TestNilReceiverIsSafe(t *testing.T) {
	var s *String
	assert.True(t, s.IsEmpty())
	assert.Nil(t, s.Reveal())
	assert.Equal(t, "", s.RevealString())
	assert.NoError(t, s.Close())
}

func TestRevealReturnsCopy(t *testing.T) {
	s := NewFromString(plain)
	a := s.Reveal()
	a[0] = 'X' // mutate the returned slice
	assert.Equal(t, plain, s.RevealString(), "internal buffer must be unaffected by mutations of returned slice")
}

func TestStringForms_AllRedacted(t *testing.T) {
	s := NewFromString(plain)

	cases := []struct {
		name string
		got  string
	}{
		{"String()", s.String()},
		{"GoString()", s.GoString()},
		{"%v", fmt.Sprintf("%v", s)},
		{"%+v", fmt.Sprintf("%+v", s)},
		{"%s", fmt.Sprintf("%s", s)},
		{"%#v", fmt.Sprintf("%#v", s)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, redactedValue, c.got)
			assert.NotContains(t, c.got, plain)
		})
	}

	// %q wraps the redacted literal in quotes.
	assert.Equal(t, `"`+redactedValue+`"`, fmt.Sprintf("%q", s))
}

func TestMarshalJSON_Redacted(t *testing.T) {
	s := NewFromString(plain)
	b, err := json.Marshal(s)
	require.NoError(t, err)
	// Go's json.Marshal HTML-escapes "<" and ">" by default. Decode the
	// emitted JSON to assert on the underlying string value rather than
	// the byte-level encoding.
	var got string
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, redactedValue, got)
	assert.NotContains(t, string(b), plain)
}

func TestMarshalJSON_NestedInStruct(t *testing.T) {
	type cfg struct {
		Token *String `json:"token"`
		Open  string  `json:"open"`
	}
	v := cfg{Token: NewFromString(plain), Open: "visible"}
	b, err := json.Marshal(v)
	require.NoError(t, err)

	var decoded struct {
		Token string `json:"token"`
		Open  string `json:"open"`
	}
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, redactedValue, decoded.Token)
	assert.Equal(t, "visible", decoded.Open)
	assert.NotContains(t, string(b), plain)
}

func TestMarshalText_Redacted(t *testing.T) {
	s := NewFromString(plain)
	b, err := s.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, redactedValue, string(b))
}

func TestSlogLogValue_Redacted(t *testing.T) {
	s := NewFromString(plain)
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("test", "secret", s)
	assert.Contains(t, buf.String(), redactedValue)
	assert.NotContains(t, buf.String(), plain)
}

func TestClose_ZeroesBuffer(t *testing.T) {
	s := NewFromString(plain)
	require.NoError(t, s.Close())
	assert.True(t, s.IsEmpty())
	assert.Empty(t, s.Reveal())
	assert.Equal(t, "", s.RevealString())
}

func TestClose_Idempotent(t *testing.T) {
	s := NewFromString(plain)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	assert.True(t, s.IsEmpty())
}

func TestConcurrentReadsAreSafe(t *testing.T) {
	s := NewFromString(plain)
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 100 {
				_ = s.RevealString()
				_ = s.IsEmpty()
				_ = s.String()
			}
		}()
	}
	for range 8 {
		<-done
	}
}
