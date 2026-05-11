package storage

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCloneCustomMeta(t *testing.T) {
	t.Parallel()
	in := map[string]string{"tenant": "acme"}
	out := CloneCustomMeta(in)

	in["tenant"] = "changed"
	out["new"] = "value"

	assert.Equal(t, "acme", out["tenant"])
	assert.NotContains(t, in, "new")
}

func TestCloneCustomMeta_PreservesNilAndEmpty(t *testing.T) {
	t.Parallel()
	assert.Nil(t, CloneCustomMeta(nil))
	assert.NotNil(t, CloneCustomMeta(map[string]string{}))
}

func TestCloneObjectMeta(t *testing.T) {
	t.Parallel()
	in := ObjectMeta{ContentType: "text/plain", Size: 3, Custom: map[string]string{"k": "v"}}
	out := CloneObjectMeta(in)

	in.Custom["k"] = "changed"
	assert.Equal(t, "v", out.Custom["k"])
	assert.Equal(t, in.ContentType, out.ContentType)
	assert.Equal(t, in.Size, out.Size)
}

func TestValidateObjectMeta(t *testing.T) {
	t.Parallel()

	err := ValidateObjectMeta(ObjectMeta{
		ContentType: "text/plain; charset=utf-8",
		Size:        12,
		Custom: map[string]string{
			"tenant-id":     "acme 42",
			ChecksumMetaKey: strings.Repeat("a", 64),
		},
	})
	assert.NoError(t, err)
}

func TestValidateObjectMetaRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta ObjectMeta
	}{
		{name: "negative size", meta: ObjectMeta{Size: -1}},
		{name: "malformed content type", meta: ObjectMeta{ContentType: "not-a-media-type"}},
		{name: "content type control character", meta: ObjectMeta{ContentType: "text/plain\r\nx: y"}},
		{name: "content type del character", meta: ObjectMeta{ContentType: "text/plain\x7f"}},
		{name: "content type invalid utf8", meta: ObjectMeta{ContentType: string([]byte{'t', 'e', 'x', 't', '/', 'p', 'l', 'a', 'i', 'n', 0xff})}},
		{name: "empty custom key", meta: ObjectMeta{Custom: map[string]string{"": "value"}}},
		{name: "custom key starts with hyphen", meta: ObjectMeta{Custom: map[string]string{"-tenant": "value"}}},
		{name: "custom key has underscore", meta: ObjectMeta{Custom: map[string]string{"tenant_id": "value"}}},
		{name: "custom value control character", meta: ObjectMeta{Custom: map[string]string{"tenant": "acme\ncorp"}}},
		{name: "custom value too long", meta: ObjectMeta{Custom: map[string]string{"tenant": strings.Repeat("x", maxCustomMetaValueLen+1)}}},
		{name: "custom metadata too large", meta: ObjectMeta{Custom: map[string]string{"tenant": strings.Repeat("x", maxCustomMetaTotalBytes+1)}}},
	}

	tooMany := ObjectMeta{Custom: make(map[string]string, maxCustomMetaEntries+1)}
	for i := 0; i < maxCustomMetaEntries+1; i++ {
		tooMany.Custom["k"+strings.Repeat("x", i+1)] = "v"
	}
	tests = append(tests, struct {
		name string
		meta ObjectMeta
	}{name: "too many custom metadata entries", meta: tooMany})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateObjectMeta(tt.meta)
			assert.True(t, errors.Is(err, ErrValidation), "got %v", err)
		})
	}
}

func TestValidateObjectMetaErrorsDoNotEchoRequestMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta ObjectMeta
		leak string
	}{
		{
			name: "content type",
			meta: ObjectMeta{ContentType: "secret-token"},
			leak: "secret-token",
		},
		{
			name: "custom key",
			meta: ObjectMeta{Custom: map[string]string{"secret_token": "value"}},
			leak: "secret_token",
		},
		{
			name: "custom value key context",
			meta: ObjectMeta{Custom: map[string]string{"secret-token": "value\n"}},
			leak: "secret-token",
		},
		{
			name: "content type length limit",
			meta: ObjectMeta{ContentType: strings.Repeat("a", maxContentTypeLen+1)},
			leak: "255",
		},
		{
			name: "custom value length limit",
			meta: ObjectMeta{Custom: map[string]string{"tenant": strings.Repeat("x", maxCustomMetaValueLen+1)}},
			leak: "1024",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateObjectMeta(tt.meta)
			assert.True(t, errors.Is(err, ErrValidation), "got %v", err)
			assert.NotContains(t, err.Error(), tt.leak)
		})
	}
}
