package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb"
	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx"
)

func TestNewPgxModule_PanicsOnEmptyDSN(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty DSN")
		}
	}()
	newPgxModule(pgxbackend.Config{})
}

func TestWithPgx_PanicsOnEmptyDSN(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty DSN")
		}
	}()
	b.WithPgx(pgxbackend.Config{})
}

func TestWithPgx_RegistersOnBuilder(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).WithPgx(pgxbackend.Config{
		DSN: "postgres://u:p@h/db?sslmode=require",
	})
	require.NotNil(t, b.pgxCfg)
	assert.NotEmpty(t, b.pgxCfg.DSN)
}

func TestValidate_RejectsPgxAndPostgresTogether(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithoutTLS().
		WithoutJWTAudience().
		WithPostgres(sqldb.Config{Host: "h", Port: 5432, User: "u", Password: "p", Name: "db",
			Options: map[string]string{"sslmode": "require"}}, sqldb.DefaultPool()).
		WithPgx(pgxbackend.Config{DSN: "postgres://u:p@h/db?sslmode=require"})

	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestValidate_AllowsPgxAlone(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithoutTLS().
		WithoutJWTAudience().
		WithPgx(pgxbackend.Config{DSN: "postgres://u:p@h/db?sslmode=require"})
	require.NoError(t, b.Validate())
}
