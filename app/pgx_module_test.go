package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
)

func TestNewPgxModule_PanicsOnEmptyDSN(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty DSN")
		}
	}()
	newPgxModule(pgxbackend.Config{}, nil)
}

func TestWithPostgres_PanicsOnEmptyDSN(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty DSN")
		}
	}()
	b.WithPostgres(pgxbackend.Config{})
}

func TestWithPostgres_RegistersOnBuilder(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).WithPostgres(pgxbackend.Config{
		DSN: "postgres://u:p@h/db?sslmode=require",
	})
	require.NotNil(t, b.pgxCfg)
	assert.NotEmpty(t, b.pgxCfg.DSN)
}

func TestValidate_AllowsPostgresAlone(t *testing.T) {
	b := New("test", "v1", validBaseConfig()).
		WithoutTLS().
		WithoutJWTAudience().
		WithPostgres(pgxbackend.Config{DSN: "postgres://u:p@h/db?sslmode=require"})
	require.NoError(t, b.Validate())
}
