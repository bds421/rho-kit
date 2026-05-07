package pgx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnect_RejectsEmptyDSN(t *testing.T) {
	_, err := Connect(context.Background(), Config{})
	require.Error(t, err)
}

func TestRequireTLS_AcceptsRequireFamily(t *testing.T) {
	for _, mode := range []string{"require", "verify-ca", "verify-full", "VERIFY-FULL"} {
		err := requireTLS("postgres://u:p@h/db?sslmode=" + mode)
		assert.NoError(t, err, "sslmode=%s must be accepted", mode)
	}
}

func TestRequireTLS_RejectsLooseModes(t *testing.T) {
	for _, mode := range []string{"prefer", "allow", "disable"} {
		err := requireTLS("postgres://u:p@h/db?sslmode=" + mode)
		assert.Error(t, err, "sslmode=%s must be rejected", mode)
	}
}

func TestRequireTLS_RejectsMissing(t *testing.T) {
	err := requireTLS("postgres://u:p@h/db")
	assert.Error(t, err)
}

func TestExtractSSLMode_URLForm(t *testing.T) {
	got := extractSSLMode("postgres://u:p@h/db?sslmode=require&application_name=svc")
	assert.Equal(t, "require", got)
}

func TestExtractSSLMode_KVForm(t *testing.T) {
	got := extractSSLMode("host=h user=u dbname=db sslmode=verify-full")
	assert.Equal(t, "verify-full", got)
}

func TestExtractSSLMode_Missing(t *testing.T) {
	assert.Equal(t, "", extractSSLMode("postgres://u:p@h/db"))
}
