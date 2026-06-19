package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifyInsertError_SeqCollisionIsTyped pins the package's documented
// promise: a (tenant_id, seq) unique-constraint violation (SQLSTATE 23505)
// from a concurrent append must surface as an error callers can detect with
// errors.Is(err, ErrSeqCollision) — without importing pgconn or matching the
// SQLSTATE themselves. Before the sentinel existed the insert path only
// wrapped a message string, so this assertion would have failed.
func TestClassifyInsertError_SeqCollisionIsTyped(t *testing.T) {
	// detail carries tenant-controlled / internal text that must not leak
	// verbatim through Error() across a trust boundary.
	pgErr := &pgconn.PgError{
		Code:           uniqueViolation,
		Message:        `duplicate key value violates unique constraint "action_log_entries_tenant_id_seq_key"`,
		ConstraintName: "action_log_entries_tenant_id_seq_key",
		Detail:         "Key (tenant_id, seq)=(tenant-secret, 1) already exists.",
	}

	got := classifyInsertError(pgErr)
	require.Error(t, got)

	// Callers can branch on the collision without touching pgconn.
	assert.ErrorIs(t, got, ErrSeqCollision)

	// The original driver error stays in the chain for triage on the log path.
	var unwrapped *pgconn.PgError
	assert.ErrorAs(t, got, &unwrapped, "underlying PgError must remain unwrappable")

	// Error() is safe to render: the sentinel text shows, the raw driver
	// detail (tenant id, key values) does not.
	msg := got.Error()
	assert.Contains(t, msg, ErrSeqCollision.Error())
	assert.NotContains(t, msg, "tenant-secret")
	assert.NotContains(t, msg, "already exists")
}

// TestClassifyInsertError_NonCollisionIsOpaque confirms that errors which are
// not a 23505 unique violation are NOT misclassified as a seq collision and
// keep the opaque "append" wrap (so callers do not retry a genuine failure).
func TestClassifyInsertError_NonCollisionIsOpaque(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "different SQLSTATE",
			err: &pgconn.PgError{
				Code:    "23502", // not_null_violation
				Message: "null value in column violates not-null constraint",
			},
		},
		{
			name: "plain non-pg error",
			err:  errors.New("connection reset by peer"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInsertError(tc.err)
			require.Error(t, got)

			assert.NotErrorIs(t, got, ErrSeqCollision,
				"only a 23505 unique violation may map to ErrSeqCollision")
			assert.True(t, strings.HasPrefix(got.Error(), "actionlog/postgres: append"),
				"non-collision insert failures keep the opaque append wrap, got %q", got.Error())
		})
	}
}

// TestClassifyInsertError_NilPassthrough guards the success path: a nil insert
// error must not be turned into a spurious failure.
func TestClassifyInsertError_NilPassthrough(t *testing.T) {
	assert.NoError(t, classifyInsertError(nil))
}
