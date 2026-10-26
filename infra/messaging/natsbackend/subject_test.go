package natsbackend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubjectToken_EscapesInjectively(t *testing.T) {
	values := []string{"", "%", "%25", "a.b", "a%2Eb", "a*b", ">", " ", "tenant-1_x", "ünï", "a\x00b", "A"}
	seen := map[string]string{}
	for _, v := range values {
		token := SubjectToken(v)
		require.NoError(t, ValidateSubject(token), "token %q of %q", token, v)
		require.NotContains(t, token, ".")
		if other, ok := seen[token]; ok {
			t.Fatalf("%q and %q share the token %q", v, other, token)
		}
		seen[token] = v
		back, err := ParseSubjectToken(token)
		require.NoError(t, err)
		assert.Equal(t, v, back)
	}
	assert.Equal(t, "tenant-1_x", SubjectToken("tenant-1_x"))
	assert.Equal(t, "a%2Eb", SubjectToken("a.b"))
	assert.Equal(t, "%25", SubjectToken("%"))
	assert.Equal(t, "%", SubjectToken(""))
}

func TestParseSubjectToken_RefusesWhatSubjectTokenNeverWrites(t *testing.T) {
	for _, token := range []string{"", "a%", "a%2", "%2e", "%41", "a.b", "%%", "a b"} {
		_, err := ParseSubjectToken(token)
		assert.ErrorIs(t, err, ErrSubjectToken, token)
	}
}

func TestSubject_JoinsEscapedTokens(t *testing.T) {
	s, err := Subject("events", "org.1", "", "case.created")
	require.NoError(t, err)
	assert.Equal(t, "events.org%2E1.%.case%2Ecreated", s)
	_, err = Subject()
	assert.ErrorIs(t, err, ErrSubjectEmpty)
}

func TestValidateSubjectAndFilter(t *testing.T) {
	cases := []struct {
		s               string
		subject, filter error
	}{
		{"events.a.b", nil, nil},
		{"events.*.b", ErrSubjectWildcard, nil},
		{"events.>", ErrSubjectWildcard, nil},
		{"events.>.b", ErrSubjectWildcard, ErrFilterWildcard},
		{"events.a*", ErrSubjectWildcard, ErrFilterWildcard},
		{"events..b", ErrSubjectToken, ErrSubjectToken},
		{".events", ErrSubjectToken, ErrSubjectToken},
		{"events.", ErrSubjectToken, ErrSubjectToken},
		{"events.a b", ErrSubjectToken, ErrSubjectToken},
		{"events.a\tb", ErrSubjectToken, ErrSubjectToken},
		{"", ErrSubjectEmpty, ErrSubjectEmpty},
		{strings.Repeat("a.", 3) + "%", nil, nil},
	}
	for _, c := range cases {
		assert.ErrorIs(t, ValidateSubject(c.s), c.subject, "subject %q", c.s)
		assert.ErrorIs(t, ValidateFilter(c.s), c.filter, "filter %q", c.s)
		if c.subject == nil {
			assert.NoError(t, ValidateSubject(c.s), c.s)
		}
		if c.filter == nil {
			assert.NoError(t, ValidateFilter(c.s), c.s)
		}
	}
}
