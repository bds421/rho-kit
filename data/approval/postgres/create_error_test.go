package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/approval"
)

func TestClassifyCreateError_DuplicateID(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:           uniqueViolation,
		Message:        `duplicate key value violates unique constraint "approval_requests_pkey"`,
		ConstraintName: "approval_requests_pkey",
		Detail:         "Key (id)=(secret-id) already exists.",
	}
	got := classifyCreateError(pgErr)
	require.Error(t, got)
	assert.ErrorIs(t, got, approval.ErrDuplicateID)
	assert.NotContains(t, got.Error(), "secret-id")
}

func TestClassifyCreateError_OtherIsOpaque(t *testing.T) {
	got := classifyCreateError(errors.New("connection reset"))
	require.Error(t, got)
	assert.NotErrorIs(t, got, approval.ErrDuplicateID)
	assert.Contains(t, got.Error(), "approval/postgres: create")
}
