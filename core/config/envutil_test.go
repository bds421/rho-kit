package config

import (
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
	t.Setenv("ENVUTIL_TEST_INT_BAD", "notanumber")
	_, err := GetInt("ENVUTIL_TEST_INT_BAD", 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid integer")
	assert.Contains(t, err.Error(), "ENVUTIL_TEST_INT_BAD")
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
	t.Setenv("ENVUTIL_TEST_BOOL_BAD", "notabool")
	_, err := GetBool("ENVUTIL_TEST_BOOL_BAD", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid boolean")
	assert.Contains(t, err.Error(), "ENVUTIL_TEST_BOOL_BAD")
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
	assert.Equal(t, "inline-value", GetSecret("MY_SECRET", ""))
}

func TestGetSecret_FromFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "my_secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("file-value\n"), 0600))

	t.Setenv("MY_SECRET_FILE", secretPath)
	t.Setenv("MY_SECRET", "should-be-ignored")
	assert.Equal(t, "file-value", GetSecret("MY_SECRET", ""))
}

func TestGetSecret_FileTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("from-file"), 0600))

	t.Setenv("PRIO_SECRET_FILE", secretPath)
	t.Setenv("PRIO_SECRET", "from-env")
	assert.Equal(t, "from-file", GetSecret("PRIO_SECRET", ""))
}

func TestGetSecret_FallbackWhenBothEmpty(t *testing.T) {
	assert.Equal(t, "default", GetSecret("MISSING_SECRET", "default"))
}

func TestGetSecret_BadFilePanics(t *testing.T) {
	t.Setenv("BAD_FILE_SECRET_FILE", "/nonexistent/path")
	t.Setenv("BAD_FILE_SECRET", "env-fallback")
	assert.Panics(t, func() {
		GetSecret("BAD_FILE_SECRET", "")
	})
}
