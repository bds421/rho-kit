package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGet_Fallback(t *testing.T) {
	assert.Equal(t, "default", Get("ENVUTIL_TEST_UNSET", "default"))
}

func TestGet_Value(t *testing.T) {
	t.Setenv("ENVUTIL_TEST_GET", "hello")
	assert.Equal(t, "hello", Get("ENVUTIL_TEST_GET", "default"))
}

func TestGetInt_Fallback(t *testing.T) {
	v, err := GetInt("ENVUTIL_TEST_UNSET", 42)
	require.NoError(t, err)
	assert.Equal(t, 42, v)
}

func TestGetInt_Value(t *testing.T) {
	t.Setenv("ENVUTIL_TEST_INT", "99")
	v, err := GetInt("ENVUTIL_TEST_INT", 0)
	require.NoError(t, err)
	assert.Equal(t, 99, v)
}

func TestGetInt_Invalid(t *testing.T) {
	t.Setenv("ENVUTIL_TEST_INT_BAD", "secret-token")
	_, err := GetInt("ENVUTIL_TEST_INT_BAD", 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid integer")
	assert.Contains(t, err.Error(), "ENVUTIL_TEST_INT_BAD")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestGetBool_Fallback(t *testing.T) {
	v, err := GetBool("ENVUTIL_TEST_UNSET", true)
	require.NoError(t, err)
	assert.True(t, v)
}

func TestGetBool_True(t *testing.T) {
	t.Setenv("ENVUTIL_TEST_BOOL", "true")
	v, err := GetBool("ENVUTIL_TEST_BOOL", false)
	require.NoError(t, err)
	assert.True(t, v)
}

func TestGetBool_False(t *testing.T) {
	t.Setenv("ENVUTIL_TEST_BOOL", "false")
	v, err := GetBool("ENVUTIL_TEST_BOOL", true)
	require.NoError(t, err)
	assert.False(t, v)
}

func TestGetBool_Invalid(t *testing.T) {
	t.Setenv("ENVUTIL_TEST_BOOL_BAD", "secret-token")
	_, err := GetBool("ENVUTIL_TEST_BOOL_BAD", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid boolean")
	assert.Contains(t, err.Error(), "ENVUTIL_TEST_BOOL_BAD")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestGetFloat64_InvalidDoesNotEchoValue(t *testing.T) {
	t.Setenv("ENVUTIL_TEST_FLOAT_BAD", "secret-token")
	_, err := GetFloat64("ENVUTIL_TEST_FLOAT_BAD", 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid float")
	assert.Contains(t, err.Error(), "ENVUTIL_TEST_FLOAT_BAD")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestParser_Int_Valid(t *testing.T) {
	t.Setenv("PARSER_INT", "42")
	var p Parser
	v := p.Int("PARSER_INT", 0)
	assert.Equal(t, 42, v)
	assert.NoError(t, p.Err())
}

func TestParser_Int_Fallback(t *testing.T) {
	var p Parser
	v := p.Int("PARSER_INT_UNSET", 99)
	assert.Equal(t, 99, v)
	assert.NoError(t, p.Err())
}

func TestParser_Int_Invalid(t *testing.T) {
	t.Setenv("PARSER_INT_BAD", "xyz")
	var p Parser
	_ = p.Int("PARSER_INT_BAD", 0)
	assert.Error(t, p.Err())
}

func TestParser_Bool_Valid(t *testing.T) {
	t.Setenv("PARSER_BOOL", "true")
	var p Parser
	v := p.Bool("PARSER_BOOL", false)
	assert.True(t, v)
	assert.NoError(t, p.Err())
}

func TestParser_Bool_Fallback(t *testing.T) {
	var p Parser
	v := p.Bool("PARSER_BOOL_UNSET", true)
	assert.True(t, v)
	assert.NoError(t, p.Err())
}

func TestParser_Bool_Invalid(t *testing.T) {
	t.Setenv("PARSER_BOOL_BAD", "nope")
	var p Parser
	_ = p.Bool("PARSER_BOOL_BAD", false)
	assert.Error(t, p.Err())
}

func TestParser_MultipleErrors(t *testing.T) {
	t.Setenv("P_INT_BAD", "x")
	t.Setenv("P_BOOL_BAD", "y")
	var p Parser
	_ = p.Int("P_INT_BAD", 0)
	_ = p.Bool("P_BOOL_BAD", false)
	err := p.Err()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "P_INT_BAD")
	assert.Contains(t, err.Error(), "P_BOOL_BAD")
}

func TestParser_NoErrors(t *testing.T) {
	var p Parser
	assert.NoError(t, p.Err())
}

func TestGetSecret_FromEnvVar(t *testing.T) {
	t.Setenv("MY_SECRET", "inline-value")
	v, err := GetSecret("MY_SECRET", "")
	require.NoError(t, err)
	assert.Equal(t, "inline-value", v)
}

func TestGetSecret_FromFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "my_secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("file-value\n"), 0600))

	t.Setenv("MY_SECRET_FILE", secretPath)
	t.Setenv("MY_SECRET", "should-be-ignored")
	v, err := GetSecret("MY_SECRET", "")
	require.NoError(t, err)
	assert.Equal(t, "file-value", v)
}

func TestGetSecret_FileTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("from-file"), 0600))

	t.Setenv("PRIO_SECRET_FILE", secretPath)
	t.Setenv("PRIO_SECRET", "from-env")
	v, err := GetSecret("PRIO_SECRET", "")
	require.NoError(t, err)
	assert.Equal(t, "from-file", v)
}

func TestGetSecret_EmptyFileDoesNotFallBackToEnv(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "empty-secret")
	require.NoError(t, os.WriteFile(secretPath, nil, 0600))

	t.Setenv("EMPTY_SECRET_FILE", secretPath)
	t.Setenv("EMPTY_SECRET", "from-env")
	v, err := GetSecret("EMPTY_SECRET", "fallback")
	require.NoError(t, err)
	assert.Equal(t, "", v)
}

func TestGetSecret_FallbackWhenBothEmpty(t *testing.T) {
	v, err := GetSecret("MISSING_SECRET", "default")
	require.NoError(t, err)
	assert.Equal(t, "default", v)
}

func TestGetSecret_BadFileReturnsError(t *testing.T) {
	t.Setenv("BAD_FILE_SECRET_FILE", "/nonexistent/secret-token")
	t.Setenv("BAD_FILE_SECRET", "env-fallback")
	_, err := GetSecret("BAD_FILE_SECRET", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unreadable")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "/nonexistent")
}

func TestGetSecret_ReadErrorDoesNotReflectFilePath(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "secret-token-dir")
	require.NoError(t, os.Mkdir(secretPath, 0o700))

	t.Setenv("DIR_SECRET_FILE", secretPath)
	t.Setenv("DIR_SECRET", "env-fallback")
	_, err := GetSecret("DIR_SECRET", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unreadable")
	assert.NotContains(t, err.Error(), "secret-token-dir")
	assert.NotContains(t, err.Error(), secretPath)
}

func TestGetSecret_RejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "huge_secret")
	require.NoError(t, os.WriteFile(secretPath, make([]byte, maxSecretFileSize+1), 0600))

	t.Setenv("HUGE_SECRET_FILE", secretPath)
	t.Setenv("HUGE_SECRET", "env-fallback")
	_, err := GetSecret("HUGE_SECRET", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.NotContains(t, err.Error(), "1048576")
	assert.NotContains(t, err.Error(), "1048577")
}

func TestMustGetSecret_BadFilePanics(t *testing.T) {
	t.Setenv("MUST_BAD_SECRET_FILE", "/nonexistent/secret-token")
	t.Setenv("MUST_BAD_SECRET", "env-fallback")
	assert.Panics(t, func() {
		MustGetSecret("MUST_BAD_SECRET", "")
	})
}

func TestMustGetSecret_BadFileLogRedactsKeyAndError(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	t.Setenv("MUST_SECRET_TOKEN_FILE", filepath.Join(t.TempDir(), "missing-secret-token"))
	assert.PanicsWithValue(t, "config: secret file is unreadable", func() {
		MustGetSecret("MUST_SECRET_TOKEN", "")
	})

	got := logs.String()
	assert.Contains(t, got, "secret file configured but unreadable")
	assert.Contains(t, got, "<redacted")
	assert.NotContains(t, got, "MUST_SECRET_TOKEN")
	assert.NotContains(t, got, "missing-secret-token")
}

func TestMustGetSecret_HappyPath(t *testing.T) {
	t.Setenv("HAPPY_MUST_SECRET", "value")
	assert.Equal(t, "value", MustGetSecret("HAPPY_MUST_SECRET", ""))
}
