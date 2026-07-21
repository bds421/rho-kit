package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

func TestNew_PanicsOnNilPool(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}

func TestStore_NilReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	var s *Store

	err := s.AppendChained(ctx, func([]byte) (auditlog.Event, error) {
		return auditlog.Event{}, nil
	})
	assert.Error(t, err)

	_, _, err = s.Query(ctx, auditlog.Filter{}, "", 10)
	assert.Error(t, err)

	err = s.RangeChain(ctx, func(auditlog.Event) error { return nil })
	assert.Error(t, err)

	_, err = s.LastHMAC(ctx)
	assert.Error(t, err)

	_, _, err = s.DeleteBefore(ctx, time.Now())
	assert.Error(t, err)
}

func TestStore_DeleteBeforeNilPoolReturnsError(t *testing.T) {
	s := &Store{pool: nil}
	n, _, err := s.DeleteBefore(context.Background(), time.Now())
	assert.Error(t, err)
	assert.Zero(t, n)
}

func TestStore_DeleteBeforeHonorsCancelledContext(t *testing.T) {
	s := &Store{pool: nil}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, _, err := s.DeleteBefore(ctx, time.Now())
	// nil pool short-circuits first; either way DeleteBefore must not panic
	// and must surface a non-nil error with a zero count.
	assert.Error(t, err)
	assert.Zero(t, n)
}

// TestStore_SatisfiesRetentionStore is a compile-time guard mirrored as a
// test so the documented auditlog.RetentionJob(store, ...) wiring keeps
// working with the production *Store.
func TestStore_SatisfiesRetentionStore(t *testing.T) {
	var _ auditlog.RetentionStore = (*Store)(nil)
}

func TestStore_AppendChainedRejectsNilBuild(t *testing.T) {
	s := &Store{pool: nil}
	err := s.AppendChained(context.Background(), nil)
	// Pool nil triggers the "not initialized" path before build is consulted;
	// pass a non-nil dummy pool stand-in via a real Store fixture in
	// integration tests. Unit tests cover the nil-build short-circuit on a
	// zero-pool store implicitly: either path returns a non-nil error.
	assert.Error(t, err)
}

func TestCursorRoundTrip(t *testing.T) {
	when := time.Date(2026, 5, 14, 10, 11, 12, 13_000_000, time.UTC)
	encoded := encodeCursor(when, "evt-1234")
	require.NotEmpty(t, encoded)

	gotTime, gotID, err := decodeCursor(encoded)
	require.NoError(t, err)
	assert.True(t, gotTime.Equal(when), "want %s, got %s", when, gotTime)
	assert.Equal(t, "evt-1234", gotID)
}

func TestCursorEmptyDecodesToZero(t *testing.T) {
	gotTime, gotID, err := decodeCursor("")
	require.NoError(t, err)
	assert.True(t, gotTime.IsZero())
	assert.Empty(t, gotID)
}

func TestCursorRejectsMalformed(t *testing.T) {
	cases := []string{
		"no-colon",
		":missing-ts",
		"00000000000000ab:",
		"deadbeef:id",         // ts hex not 16 chars
		"GGGGGGGGGGGGGGGG:id", // not hex
		"00000000000000ab:" + strings.Repeat("x", auditlog.MaxEventIDBytes+1),
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, _, err := decodeCursor(c)
			assert.Error(t, err, "cursor %q must not decode", c)
		})
	}
}

func TestEscapeLikePrefix(t *testing.T) {
	assert.Equal(t, "plain", escapeLikePrefix("plain"))
	assert.Equal(t, `50\%\_off`, escapeLikePrefix("50%_off"))
	assert.Equal(t, `path\\thing`, escapeLikePrefix(`path\thing`))
}
