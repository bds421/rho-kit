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

func TestRequireTLSInProd_AcceptsRequireFamily(t *testing.T) {
	t.Setenv("KIT_ENV", "production")
	for _, mode := range []string{"require", "verify-ca", "verify-full", "VERIFY-FULL"} {
		err := requireTLSInProd("postgres://u:p@h/db?sslmode=" + mode)
		assert.NoError(t, err, "sslmode=%s must be accepted", mode)
	}
}

func TestRequireTLSInProd_RejectsLooseModes(t *testing.T) {
	t.Setenv("KIT_ENV", "production")
	for _, mode := range []string{"prefer", "allow", "disable"} {
		err := requireTLSInProd("postgres://u:p@h/db?sslmode=" + mode)
		assert.Error(t, err, "sslmode=%s must be rejected", mode)
	}
}

func TestRequireTLSInProd_RejectsMissing(t *testing.T) {
	t.Setenv("KIT_ENV", "production")
	err := requireTLSInProd("postgres://u:p@h/db")
	assert.Error(t, err)
}

func TestRequireTLSInProd_AllowsAnythingInDev(t *testing.T) {
	t.Setenv("KIT_ENV", "development")
	for _, mode := range []string{"", "disable", "prefer"} {
		err := requireTLSInProd("postgres://u:p@h/db?sslmode=" + mode)
		assert.NoError(t, err, "dev should not enforce sslmode (got error for %q)", mode)
	}
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
